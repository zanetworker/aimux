package runtime

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// K8sBackend manages Kubernetes Deployments as agent execution environments.
// It implements the Backend interface and provides additional methods for
// pod-level operations (CreateAndWait, ScaleDownOne).
//
// Thread-safe: the clientset is lazily initialized behind a mutex.
type K8sBackend struct {
	namespace  string
	kubeconfig string
	mu         sync.Mutex
	clientset  kubernetes.Interface
}

// NewK8sBackend creates a K8sBackend with the given namespace and kubeconfig path.
// If namespace is empty, it defaults to "agents".
// If kubeconfig is empty, in-cluster config is tried first, then default kubeconfig locations.
func NewK8sBackend(namespace, kubeconfig string) *K8sBackend {
	if namespace == "" {
		namespace = "agents"
	}
	return &K8sBackend{
		namespace:  namespace,
		kubeconfig: kubeconfig,
	}
}

// Name returns "kubernetes".
func (b *K8sBackend) Name() string { return "kubernetes" }

// IsRemote returns true; K8s workloads run on a remote cluster.
func (b *K8sBackend) IsRemote() bool { return true }

// Create ensures the deployment exists and scales it up by 1 replica.
// If the deployment does not exist, it is auto-created with the image
// and labels from opts. The deployment starts at 0 replicas and is then
// scaled up.
func (b *K8sBackend) Create(name string, opts BackendCreateOpts) error {
	client, err := b.KubeClient()
	if err != nil {
		return fmt.Errorf("cannot connect to cluster: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	deploy, err := client.AppsV1().Deployments(b.namespace).Get(ctx, name, metav1.GetOptions{})
	if errors.IsNotFound(err) {
		deploy, err = b.createDeployment(ctx, client, name, opts)
		if err != nil {
			return fmt.Errorf("cannot create deployment %q: %w", name, err)
		}
	} else if err != nil {
		return fmt.Errorf("cannot get deployment %q: %w", name, err)
	}

	// Scale up by 1.
	replicas := int32(1)
	if deploy.Spec.Replicas != nil {
		replicas = *deploy.Spec.Replicas + 1
	}
	deploy.Spec.Replicas = &replicas
	_, err = client.AppsV1().Deployments(b.namespace).Update(ctx, deploy, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("cannot scale %q: %w", name, err)
	}
	return nil
}

// Start scales the deployment up by 1 (same as Create with empty opts).
func (b *K8sBackend) Start(name string) error {
	return b.Create(name, BackendCreateOpts{})
}

// Stop scales the deployment to 0 replicas.
func (b *K8sBackend) Stop(name string) error {
	client, err := b.KubeClient()
	if err != nil {
		return fmt.Errorf("cannot connect to cluster: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	deploy, err := client.AppsV1().Deployments(b.namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("deployment %q not found: %w", name, err)
	}

	zero := int32(0)
	deploy.Spec.Replicas = &zero
	_, err = client.AppsV1().Deployments(b.namespace).Update(ctx, deploy, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("cannot scale down %q: %w", name, err)
	}
	return nil
}

// Delete scales the deployment down by 1 replica (minimum 0).
func (b *K8sBackend) Delete(name string) error {
	return b.ScaleDownOne(name)
}

// Status checks the pod phase of pods belonging to the named deployment.
// Returns StateRunning if any pod is Running, StateCreating if pods are
// Pending, StateError if all pods are in error states, and StateStopped
// if there are no pods (replicas=0).
func (b *K8sBackend) Status(name string) (State, error) {
	client, err := b.KubeClient()
	if err != nil {
		return StateError, fmt.Errorf("cannot connect to cluster: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	pods, err := client.CoreV1().Pods(b.namespace).List(ctx, metav1.ListOptions{
		LabelSelector: "app=" + name,
	})
	if err != nil {
		return StateError, fmt.Errorf("cannot list pods for %q: %w", name, err)
	}

	if len(pods.Items) == 0 {
		return StateStopped, nil
	}

	for _, pod := range pods.Items {
		if pod.Status.Phase == corev1.PodRunning {
			return StateRunning, nil
		}
	}
	for _, pod := range pods.Items {
		if pod.Status.Phase == corev1.PodPending {
			return StateCreating, nil
		}
	}
	return StateError, nil
}

// ExecPrefix returns the kubectl exec command prefix for the named pod.
func (b *K8sBackend) ExecPrefix(name string) []string {
	return []string{"kubectl", "exec", "-it", name, "-n", b.namespace, "--"}
}

// CreateAndWait creates a deployment (via Create) and then polls for up
// to 60 seconds until a new Running pod appears. Returns the pod name.
func (b *K8sBackend) CreateAndWait(name string, opts BackendCreateOpts) (podName string, err error) {
	client, err := b.KubeClient()
	if err != nil {
		return "", fmt.Errorf("cannot connect to cluster: %w", err)
	}

	// Snapshot existing pod names before scaling.
	existingPods := make(map[string]bool)
	podList, listErr := client.CoreV1().Pods(b.namespace).List(context.Background(), metav1.ListOptions{
		LabelSelector: "app=" + name,
	})
	if listErr == nil {
		for _, p := range podList.Items {
			existingPods[p.Name] = true
		}
	}

	// Scale up.
	if err := b.Create(name, opts); err != nil {
		return "", err
	}

	// Poll for the new pod.
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return "", fmt.Errorf("timed out waiting for pod to start (60s)")
		case <-ticker.C:
			pods, err := client.CoreV1().Pods(b.namespace).List(ctx, metav1.ListOptions{
				LabelSelector: "app=" + name,
				FieldSelector: "status.phase=Running",
			})
			if err != nil {
				continue
			}
			for _, p := range pods.Items {
				if !existingPods[p.Name] {
					return p.Name, nil
				}
			}
		}
	}
}

// ScaleDownOne decrements the replica count of the named deployment by 1
// (minimum 0).
func (b *K8sBackend) ScaleDownOne(name string) error {
	client, err := b.KubeClient()
	if err != nil {
		return fmt.Errorf("cannot connect to cluster: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	deploy, err := client.AppsV1().Deployments(b.namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("deployment %q not found: %w", name, err)
	}

	current := int32(1)
	if deploy.Spec.Replicas != nil {
		current = *deploy.Spec.Replicas
	}
	desired := current - 1
	if desired < 0 {
		desired = 0
	}
	deploy.Spec.Replicas = &desired
	_, err = client.AppsV1().Deployments(b.namespace).Update(ctx, deploy, metav1.UpdateOptions{})
	return err
}

// KubeClient returns the cached kubernetes.Interface, creating it lazily
// on first call. Thread-safe via mutex.
func (b *K8sBackend) KubeClient() (kubernetes.Interface, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.clientset != nil {
		return b.clientset, nil
	}

	var restCfg *rest.Config
	var err error

	if b.kubeconfig != "" {
		restCfg, err = clientcmd.BuildConfigFromFlags("", b.kubeconfig)
	} else {
		restCfg, err = rest.InClusterConfig()
		if err != nil {
			loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
			restCfg, err = clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
				loadingRules,
				&clientcmd.ConfigOverrides{},
			).ClientConfig()
		}
	}
	if err != nil {
		return nil, fmt.Errorf("load kubeconfig: %w", err)
	}

	clientset, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		return nil, fmt.Errorf("create kubernetes clientset: %w", err)
	}
	b.clientset = clientset
	return b.clientset, nil
}

// SetClientset allows injecting a fake clientset for testing.
func (b *K8sBackend) SetClientset(cs kubernetes.Interface) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.clientset = cs
}

// Namespace returns the configured namespace.
func (b *K8sBackend) Namespace() string { return b.namespace }

// createDeployment builds and creates a Deployment in the backend's namespace.
// The deployment starts at 0 replicas (caller scales up afterward).
func (b *K8sBackend) createDeployment(ctx context.Context, clientset kubernetes.Interface, name string, opts BackendCreateOpts) (*appsv1.Deployment, error) {
	// Ensure the namespace exists.
	b.ensureNamespace(ctx, clientset)

	// Ensure auth secrets exist from local env vars.
	b.ensureAuthSecrets(ctx, clientset)

	image := opts.Image
	if image == "" {
		image = "quay.io/azaalouk/agent-session:latest"
	}

	replicas := int32(0)

	labels := map[string]string{
		"app":                          name,
		"app.kubernetes.io/part-of":    "k8s-agents",
		"app.kubernetes.io/managed-by": "aimux",
	}
	for k, v := range opts.Labels {
		labels[k] = v
	}

	var envVars []corev1.EnvVar
	// Vertex AI auth via mounted ADC file.
	envVars = append(envVars, corev1.EnvVar{
		Name: "GOOGLE_APPLICATION_CREDENTIALS", Value: "/var/secrets/gcp/adc.json",
	})
	// API key auth via secret.
	envVars = append(envVars, corev1.EnvVar{
		Name: "ANTHROPIC_API_KEY",
		ValueFrom: &corev1.EnvVarSource{
			SecretKeyRef: &corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: "llm-keys"},
				Key:                  "anthropic",
				Optional:             boolPtr(true),
			},
		},
	})
	envVars = append(envVars, corev1.EnvVar{Name: "TERM", Value: "xterm-256color"})
	for k, v := range opts.Env {
		envVars = append(envVars, corev1.EnvVar{Name: k, Value: v})
	}

	// Resource defaults.
	cpuReq := "500m"
	cpuLim := "1000m"
	memReq := "1Gi"
	memLim := "2Gi"
	if opts.Resources != nil {
		if opts.Resources.CPULimit != "" {
			cpuLim = opts.Resources.CPULimit
		}
		if opts.Resources.MemoryLimit != "" {
			memLim = opts.Resources.MemoryLimit
		}
	}

	// Extract container name from deployment name (last segment after "agent-").
	containerName := name
	if parts := splitLast(name, "-"); parts != "" {
		containerName = parts
	}

	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: b.namespace,
			Labels:    labels,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app": name},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labels,
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Name:    containerName,
						Image:   image,
						Command: []string{"sleep", "infinity"},
						Env:     envVars,
						Resources: corev1.ResourceRequirements{
							Requests: corev1.ResourceList{
								corev1.ResourceMemory: resource.MustParse(memReq),
								corev1.ResourceCPU:    resource.MustParse(cpuReq),
							},
							Limits: corev1.ResourceList{
								corev1.ResourceMemory: resource.MustParse(memLim),
								corev1.ResourceCPU:    resource.MustParse(cpuLim),
							},
						},
						VolumeMounts: []corev1.VolumeMount{
							{Name: "gcp-adc", MountPath: "/var/secrets/gcp", ReadOnly: true},
						},
					}},
					Volumes: []corev1.Volume{
						{Name: "gcp-adc", VolumeSource: corev1.VolumeSource{
							Secret: &corev1.SecretVolumeSource{
								SecretName: "gcp-adc",
								Optional:   boolPtr(true),
							},
						}},
					},
				},
			},
		},
	}

	return clientset.AppsV1().Deployments(b.namespace).Create(ctx, deploy, metav1.CreateOptions{})
}

// ensureNamespace creates the namespace if it does not exist.
func (b *K8sBackend) ensureNamespace(ctx context.Context, clientset kubernetes.Interface) {
	_, err := clientset.CoreV1().Namespaces().Get(ctx, b.namespace, metav1.GetOptions{})
	if errors.IsNotFound(err) {
		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name:   b.namespace,
				Labels: map[string]string{"app.kubernetes.io/managed-by": "aimux"},
			},
		}
		_, _ = clientset.CoreV1().Namespaces().Create(ctx, ns, metav1.CreateOptions{})
	}
}

// ensureAuthSecrets creates auth secrets from local environment if they
// do not already exist in the cluster.
func (b *K8sBackend) ensureAuthSecrets(ctx context.Context, clientset kubernetes.Interface) {
	// GCP ADC: copy local credentials file into a secret.
	if adcPath := os.Getenv("GOOGLE_APPLICATION_CREDENTIALS"); adcPath != "" {
		_, err := clientset.CoreV1().Secrets(b.namespace).Get(ctx, "gcp-adc", metav1.GetOptions{})
		if errors.IsNotFound(err) {
			data, readErr := os.ReadFile(adcPath) // #nosec G304,G703 -- path from trusted env var
			if readErr == nil {
				secret := &corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "gcp-adc",
						Namespace: b.namespace,
						Labels:    map[string]string{"app.kubernetes.io/managed-by": "aimux"},
					},
					Data: map[string][]byte{"adc.json": data},
				}
				_, _ = clientset.CoreV1().Secrets(b.namespace).Create(ctx, secret, metav1.CreateOptions{})
			}
		}
	}

	// API key: create llm-keys secret from env.
	if apiKey := os.Getenv("ANTHROPIC_API_KEY"); apiKey != "" {
		_, err := clientset.CoreV1().Secrets(b.namespace).Get(ctx, "llm-keys", metav1.GetOptions{})
		if errors.IsNotFound(err) {
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "llm-keys",
					Namespace: b.namespace,
					Labels:    map[string]string{"app.kubernetes.io/managed-by": "aimux"},
				},
				Data: map[string][]byte{"anthropic": []byte(apiKey)},
			}
			_, _ = clientset.CoreV1().Secrets(b.namespace).Create(ctx, secret, metav1.CreateOptions{})
		}
	}
}

func boolPtr(b bool) *bool { return &b }

// splitLast returns the last segment after the final hyphen, or "" if no hyphen.
func splitLast(s, sep string) string {
	for i := len(s) - 1; i >= 0; i-- {
		if string(s[i]) == sep {
			return s[i+1:]
		}
	}
	return ""
}
