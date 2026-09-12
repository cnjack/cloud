#!/usr/bin/env python3
"""Register immutable Cube runner templates after the images workflow passes.

Produces a ConfigMap merge patch. --apply applies only these keys, preserving
all other cluster configuration and credentials. No API key is printed.
"""
import argparse
import concurrent.futures
import json
import os
from pathlib import Path
import re
import shlex
import subprocess
import time
import urllib.error
import urllib.request

PROFILES = ("default", "go-node", "python", "rust", "polyglot")


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("--release", required=True, help="immutable published version, e.g. v0.0.150")
    p.add_argument("--api", default="http://192.168.10.194:31000")
    p.add_argument("--ssh", default="root@192.168.10.194")
    p.add_argument("--registry", default="registry.cn-shanghai.aliyuncs.com/jcode-cloud")
    p.add_argument("--owner", default="jcloud-company")
    p.add_argument("--context", default="wangwenhui@local")
    p.add_argument("--namespace", default="jcode")
    p.add_argument("--output", default="deploy/cubesandbox/runtime-config.generated.json")
    p.add_argument("--apply", action="store_true")
    p.add_argument("--workers", type=int, default=1, help="bounded template build concurrency")
    p.add_argument("--retry-failed", action="store_true", help="redo a recorded failed build after its cause is repaired")
    args = p.parse_args()
    if not re.fullmatch(r"v\d+\.\d+\.\d+", args.release):
        p.error("--release must be an immutable version")
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))

    def api(path):
        headers = {}
        if os.environ.get("CUBE_API_KEY"):
            headers["Authorization"] = "Bearer " + os.environ["CUBE_API_KEY"]
        with opener.open(urllib.request.Request(args.api.rstrip("/") + path, headers=headers), timeout=30) as r:
            return json.load(r)

    existing = api("/templates")

    def cli(arguments):
        command = ["kubectl", "exec", "-n", "cubesandbox", "deployment/cube-cubemastercli", "--",
                   "cubemastercli", "--address", "cube-master", "--timeout", "60s", "tpl", *arguments, "--json"]
        result = subprocess.run(["ssh", "-o", "BatchMode=yes", args.ssh, shlex.join(command)],
                                capture_output=True, text=True, check=True)
        return json.loads(result.stdout)

    def register(profile):
        image = f"{args.registry}/jcloud-cube-{profile}:{args.release}"
        alias = f"jcloud-{profile}-{args.release.replace('.', '-')}"
        receipt_path = Path(args.output).with_name(f"template-{profile}-{args.release}.generated.json")
        receipt = json.loads(receipt_path.read_text()) if receipt_path.exists() else None
        matches = [t for t in existing if alias in t.get("aliases", [])]
        if len(matches) > 1:
            raise RuntimeError(f"ambiguous template alias {alias}")
        if matches:
            template_id = matches[0]["templateID"]
            job_id = matches[0].get("jobID", "")
            if matches[0].get("imageInfo", "").split("@")[0] != image:
                raise RuntimeError(f"template {alias} points at a different image")
        elif receipt:
            if receipt["image"] != image:
                raise RuntimeError(f"build receipt for {profile} points at a different image")
            template_id, job_id = receipt["template_id"], receipt["job_id"]
        else:
            command = ["create-from-image",
                       "--image", image, "--alias", alias, "--writable-layer-size", "32Gi",
                       "--expose-port", "49983", "--probe", "49983", "--probe-path", "/health",
                       "--allow-internet-access", "--allow-out-cidr", "192.168.10.236/32",
                       "--dns", "223.5.5.5", "--cpu", "2000", "--memory", "4096", "--detach"]
            response = cli(command)
            template_id = response["job"]["template_id"]
            job_id = response["job"]["job_id"]
        def save_receipt():
            receipt_path.parent.mkdir(parents=True, exist_ok=True)
            receipt_path.write_text(json.dumps({"image": image, "template_id": template_id, "job_id": job_id}, indent=2) + "\n")
        save_receipt()
        print(f"{profile}: tracking {template_id} job={job_id}", flush=True)
        deadline = time.monotonic() + 900
        last_phase = None
        retried = False
        while time.monotonic() < deadline:
            # CubeAPI does not publish a usable template while its native build
            # is in progress. A 404 here is pending, not proof that creation failed.
            try:
                template = api("/templates/" + template_id)
            except urllib.error.HTTPError as error:
                if error.code != 404 and error.code < 500:
                    raise
                template = {}
            state = template.get("status")
            if state == "READY":
                print(f"{profile}: READY {template_id}", flush=True)
                return profile, template_id
            if job_id:
                job = cli(["status", "--job-id", job_id])["job"]
                phase = (job.get("status"), job.get("phase"))
                if phase != last_phase:
                    print(f"{profile}: {phase[0]} {phase[1]}", flush=True)
                    last_phase = phase
                if job.get("status") == "FAILED":
                    if args.retry_failed and not retried:
                        retry = cli(["redo", "--template-id", template_id, "--detach"])["job"]
                        job_id = retry["job_id"]
                        save_receipt()
                        retried = True
                    else:
                        raise RuntimeError(f"{profile}: template build failed; inspect {template_id} in CubeSandbox")
            elif state == "FAILED":
                raise RuntimeError(f"{profile}: template build failed; inspect {template_id} in CubeSandbox")
            time.sleep(5)
        raise RuntimeError(f"{profile}: timed out waiting for {template_id}")

    if not 1 <= args.workers <= 2:
        p.error("--workers must be 1 or 2")
    if args.workers == 1:
        # Stop on the first failure instead of starting queued builds while
        # ThreadPoolExecutor's context manager waits for shutdown.
        templates = dict(register(profile) for profile in PROFILES)
    else:
        executor = concurrent.futures.ThreadPoolExecutor(max_workers=args.workers)
        try:
            templates = dict(executor.map(register, PROFILES))
        finally:
            executor.shutdown(wait=True, cancel_futures=True)
    profiles = {p: f"{args.registry}/jcloud-runner-{p}:{args.release}" for p in PROFILES if p != "default"}
    patch = {"data": {
        "JOB_LAUNCHER": "cubesandbox", "CUBE_API_URL": args.api,
        "CUBE_PROXY_NODE_IP": "192.168.10.194", "CUBE_PROXY_PORT_HTTP": "30080",
        "CUBE_RUNTIME_OWNER": args.owner,
        "CUBE_RUNNER_TEMPLATES_JSON": json.dumps(templates, separators=(",", ":")),
        "RUNNER_IMAGE": f"{args.registry}/jcloud-runner:{args.release}",
        "RUNNER_PROFILES_JSON": json.dumps(profiles, separators=(",", ":")),
        "ORCH_BASE_URL": "https://cloud.j-code.net",
    }}
    output = Path(args.output)
    output.parent.mkdir(parents=True, exist_ok=True)
    output.write_text(json.dumps(patch, indent=2) + "\n")
    print(f"runtime ConfigMap patch saved: {output}", flush=True)
    if args.apply:
        env = dict(os.environ, NO_PROXY="192.168.10.221", no_proxy="192.168.10.221")
        subprocess.run(["kubectl", "patch", "configmap", "orchestrator-config", "--context", args.context,
                        "-n", args.namespace, "--type", "merge", "--patch-file", str(output)], env=env, check=True)


if __name__ == "__main__":
    main()
