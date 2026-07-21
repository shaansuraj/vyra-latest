# Kubernetes Placeholder Manifests

These manifests are a phase-2 starting point for Kubernetes parity with the
current Docker Compose deployment. They are intentionally minimal and should be
customized before production use.

## Included

- `namespace.yaml` shared namespace
- `relay-deployment.yaml` relay API + process manager
- `app-deployment.yaml` frontend app
- `engine-adapter-deployment.yaml` adapter runtime
- `cv-worker-deployment.yaml` shared CV worker pool placeholder
- `llm-worker-deployment.yaml` Ask-AI worker placeholder
- `kustomization.yaml` simple compose target

## Next steps

1. Replace image names with your registry images.
2. Add persistent volumes for snapshots, custom models, and relay storage.
3. Add GPU node selectors/tolerations for CV/LLM workloads.
4. Add HPA rules per workload and tune resource limits.
5. Add secrets/config maps for runtime env values.
