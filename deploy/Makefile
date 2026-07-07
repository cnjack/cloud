# jcode Cloud Agent — MVP deployment onto the LOCAL OrbStack k8s cluster.
#
# Scope: cloud/deploy/ only. This Makefile never touches any kubectl context
# other than `orbstack` (see the guard in `_ctx-guard`), and never runs `git
# commit`. It builds images with the local Docker daemon (OrbStack shares its
# image store with its k8s, so imagePullPolicy: Never/IfNotPresent works with
# no registry push).
#
# Layout:
#   base/postgres/     Postgres Deployment+PVC+Service+Secret
#   base/orchestrator/  orchestrator Deployment+Service+SA+RBAC+ConfigMap/Secret
#                       (+ -migrate-only initContainer)
#   base/mockllm/       scripted OpenAI-compatible LLM (own image, see
#                       deploy/mockllm/Dockerfile) so runner Jobs can complete
#                       an agent loop with no real API key
#   base/gitseed/       tiny in-cluster git:// server + seeded bare repo, so
#                       runner Jobs have a real clonable REPO_URL
#
# Usage:
#   make build          build all three local images
#   make up             kubectl apply -k (waits for rollouts)
#   make down            kubectl delete -k
#   make port-forward   orchestrator :8080 -> localhost:8080 (foreground)
#   make port-forward-gitea  Gitea :3000 -> localhost:3000 (foreground; OAuth login)
#   make seed-repo       (re)run the git-seed initContainer's job manually / verify it
#   make smoke          scripted e2e smoke test against the live cluster
#   make logs-orch       tail orchestrator logs

SHELL := /bin/bash
.SHELLFLAGS := -eu -o pipefail -c

KCTX        := orbstack
NAMESPACE   := jcloud
CLOUD_ROOT  := $(abspath ..)
RUNNER_DIR  := $(CLOUD_ROOT)/runner
ORCH_DIR    := $(CLOUD_ROOT)/orchestrator

ORCH_IMAGE    := jcloud/orchestrator:dev
RUNNER_IMAGE  := jcloud/runner:dev
MOCKLLM_IMAGE := jcloud/mockllm:dev
GITSEED_IMAGE := jcloud/gitseed:dev

.PHONY: help
help: ## Show this help.
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}'

# --- context guard: refuse to run against anything but orbstack -------------
.PHONY: _ctx-guard
_ctx-guard:
	@cur="$$(kubectl config current-context 2>/dev/null || true)"; \
	if [ "$$cur" != "$(KCTX)" ]; then \
		echo "refusing to run: current kubectl context is '$$cur', expected '$(KCTX)'"; \
		echo "run: kubectl config use-context $(KCTX)"; \
		exit 1; \
	fi

# --- build --------------------------------------------------------------
.PHONY: build build-orchestrator build-runner build-mockllm build-gitseed
build: build-orchestrator build-runner build-mockllm build-gitseed ## Build all local images (orchestrator, runner, mockllm, gitseed).

build-orchestrator: ## Build jcloud/orchestrator:dev from cloud/orchestrator/Dockerfile.
	docker build -t $(ORCH_IMAGE) "$(ORCH_DIR)"

build-runner: ## Cross-compile + build jcloud/runner:dev via cloud/runner/build.sh.
	SKIP_DOCKER=1 "$(RUNNER_DIR)/build.sh"
	docker build --platform "linux/$$( [ "$$(uname -m)" = arm64 ] && echo arm64 || echo amd64 )" \
		-t $(RUNNER_IMAGE) "$(RUNNER_DIR)"

build-mockllm: ## Build jcloud/mockllm:dev from deploy/mockllm/Dockerfile (build context = cloud/, source untouched).
	docker build -f mockllm/Dockerfile -t $(MOCKLLM_IMAGE) "$(CLOUD_ROOT)"

build-gitseed: ## Build jcloud/gitseed:dev (alpine + git-daemon) for the in-cluster seed repo server.
	docker build -t $(GITSEED_IMAGE) gitseed

# --- deploy --------------------------------------------------------------
.PHONY: up down restart
up: _ctx-guard ## kubectl apply -k the manifests and wait for rollouts.
	kubectl apply -f base/namespace.yaml
	kubectl apply -k .
	kubectl -n $(NAMESPACE) rollout status deploy/postgres --timeout=120s
	kubectl -n $(NAMESPACE) rollout status deploy/mockllm --timeout=60s
	kubectl -n $(NAMESPACE) rollout status deploy/git-seed --timeout=60s
	kubectl -n $(NAMESPACE) rollout status deploy/gitea --timeout=180s
	kubectl -n $(NAMESPACE) rollout status deploy/orchestrator --timeout=120s
	@echo "waiting for the gitea-bootstrap Job (org/repo/token + orchestrator secret)..."
	kubectl -n $(NAMESPACE) wait --for=condition=complete job/gitea-bootstrap --timeout=180s || \
		{ echo "gitea-bootstrap did not complete; logs:"; kubectl -n $(NAMESPACE) logs job/gitea-bootstrap --tail=40 || true; }
	@echo "restarting orchestrator so it picks up GITEA_URL/GITEA_TOKEN from the bootstrap Secret..."
	kubectl -n $(NAMESPACE) rollout restart deploy/orchestrator
	kubectl -n $(NAMESPACE) rollout status deploy/orchestrator --timeout=120s
	@echo "jcloud is up (Gitea draft-PR enabled). Run 'make port-forward' then point the console dev server at localhost:8080."

down: _ctx-guard ## Delete everything this manifest set created (namespace itself is left in place; see base/kustomization.yaml note).
	kubectl delete -k . --ignore-not-found

restart: down up ## Convenience: delete then reapply.

# --- dev workflow --------------------------------------------------------
.PHONY: port-forward
port-forward: _ctx-guard ## Foreground port-forward orchestrator :8080 -> localhost:8080.
	kubectl -n $(NAMESPACE) port-forward svc/orchestrator 8080:8080

.PHONY: port-forward-gitea
port-forward-gitea: _ctx-guard ## Foreground port-forward Gitea :3000 -> localhost:3000 (OAuth login + browsing the repo UI). Run alongside 'make port-forward' in a separate terminal.
	kubectl -n $(NAMESPACE) port-forward svc/gitea 3000:3000

.PHONY: seed-repo
seed-repo: _ctx-guard ## Verify/re-trigger the in-cluster git seed repo.
	@echo "git-seed serves git://git.$(NAMESPACE).svc.cluster.local/seed.git (bare repo) via git-daemon --export-all."
	@echo "Re-seeding (safe/idempotent -- seed.sh no-ops if already initialized):"
	kubectl -n $(NAMESPACE) rollout restart deploy/git-seed
	kubectl -n $(NAMESPACE) rollout status deploy/git-seed --timeout=60s
	@pod=$$(kubectl -n $(NAMESPACE) get pod -l app.kubernetes.io/name=git-seed -o jsonpath='{.items[0].metadata.name}'); \
	echo "checking clone from a throwaway debug pod..."; \
	kubectl -n $(NAMESPACE) run gitseed-check-$$RANDOM --rm -i --restart=Never --image=alpine/git:latest --command -- \
		sh -c "git clone --quiet git://git.$(NAMESPACE).svc.cluster.local/seed.git /tmp/check && cat /tmp/check/README.md"

.PHONY: gitea-bootstrap
gitea-bootstrap: _ctx-guard ## Re-run the Gitea bootstrap Job (org/repo/token + orchestrator secret). Idempotent.
	kubectl -n $(NAMESPACE) delete job/gitea-bootstrap --ignore-not-found
	kubectl -n $(NAMESPACE) apply -k base/gitea
	kubectl -n $(NAMESPACE) wait --for=condition=complete job/gitea-bootstrap --timeout=180s
	kubectl -n $(NAMESPACE) rollout restart deploy/orchestrator
	@echo "gitea-bootstrap complete; orchestrator restarted to pick up GITEA_TOKEN."

.PHONY: logs-orch
logs-orch: _ctx-guard ## Tail orchestrator logs.
	kubectl -n $(NAMESPACE) logs deploy/orchestrator -f

.PHONY: logs-mockllm
logs-mockllm: _ctx-guard ## Tail mockllm logs.
	kubectl -n $(NAMESPACE) logs deploy/mockllm -f

.PHONY: status
status: _ctx-guard ## Show pods/jobs/services in the namespace.
	kubectl -n $(NAMESPACE) get deploy,pods,svc,jobs

.PHONY: smoke
smoke: _ctx-guard ## Run the scripted smoke test (see smoke-test.sh) against the live cluster.
	./smoke-test.sh
