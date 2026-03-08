FROM registry.access.redhat.com/ubi9/python-312

# Install system packages as root
USER root

RUN dnf install -y git --setopt=install_weak_deps=0 && \
    dnf clean all

# Build context is aimux repo root — paths reflect the runtime/ layout
COPY runtime/coordinator/ /opt/app-root/src/coordinator/
COPY runtime/agents/gemini/ /opt/app-root/src/agent/

# Give uid 1001 (OpenShift default) ownership of the app directory
RUN chown -R 1001:0 /opt/app-root/src/

# Drop back to non-root before installing Python deps
USER 1001

# PYTHONPATH ensures `import coordinator` resolves to /opt/app-root/src/coordinator/
ENV PYTHONPATH="/opt/app-root/src"
# Disable Python output buffering so logs appear immediately in kubectl logs
ENV PYTHONUNBUFFERED=1

RUN pip install --no-cache-dir google-genai redis

CMD ["python", "/opt/app-root/src/agent/main.py"]
# Role behaviour controlled entirely by env vars:
#   ROLE=researcher  MODEL=gemini-2.0-flash
#   ROLE=coder       MODEL=gemini-2.5-pro
