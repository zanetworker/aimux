// @ts-check
import { defineConfig } from 'astro/config';
import starlight from '@astrojs/starlight';

export default defineConfig({
	site: 'https://zanetworker.github.io',
	base: '/aimux',
	integrations: [
		starlight({
			title: 'aimux',
			description: 'Tame the agent sprawl. See all your agents. Trace what they did. Judge if it was good.',
			social: [{ icon: 'github', label: 'GitHub', href: 'https://github.com/zanetworker/aimux' }],
			customCss: ['./src/styles/custom.css'],
			sidebar: [
				{ label: 'Home', slug: '' },
				{ label: 'Getting Started', slug: 'getting-started' },
				{ label: 'Configuration', slug: 'configuration' },
				{
					label: 'Guides',
					items: [
						{ label: 'Web Dashboard', slug: 'guides/web-dashboard' },
						{ label: 'TUI Keybindings', slug: 'guides/tui-keybindings' },
						{ label: 'Launch Modes', slug: 'guides/launch-modes' },
						{ label: 'Tracing & Annotations', slug: 'guides/tracing' },
						{ label: 'Cost Tracking', slug: 'guides/cost-tracking' },
						{ label: 'Notifications', slug: 'guides/notifications' },
						{ label: 'Plugins', slug: 'guides/plugins' },
						{ label: 'Tasks Integration', slug: 'guides/tasks' },
						{ label: 'MLflow Integration', slug: 'guides/mlflow-integration' },
					],
				},
				{
					label: 'Advanced',
					items: [
						{ label: 'Adding a Provider', slug: 'advanced/adding-a-provider' },
						{ label: 'Kubernetes Quickstart', slug: 'advanced/k8s-quickstart' },
					],
				},
			],
		}),
	],
});
