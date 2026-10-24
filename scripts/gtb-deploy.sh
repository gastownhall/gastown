#!/usr/bin/env bash
# Build the gtb server image, push it to the nhaboc Nexus registry and
# (re)deploy it to k3s. Run on the k3s host (beboc) as a user with sudo,
# docker and kubectl:
#   curl -fsSL https://raw.githubusercontent.com/baovo15/gtb/main/scripts/gtb-deploy.sh | bash
# Optional: GTB_ADMIN_TOKEN=<32+ chars> to set the dashboard admin token from
# the Secret gtb-server instead of letting the server generate one.
# Re-running it is a redeploy: it pulls main, builds a new tag and rolls the
# gtb-server StatefulSet. The store under /var/lib/gtb/server survives.
# It never deletes anything: the old Gas Town objects are listed at the end.
set -euo pipefail

REPO_URL=${REPO_URL:-https://github.com/baovo15/gtb.git}
SRC=${SRC:-$HOME/src/gtb}
REGISTRY=${REGISTRY:-127.0.0.1:30082}
NEXUS_USER=${NEXUS_USER:-ci}
NEXUS_PASSWORD_FILE=${NEXUS_PASSWORD_FILE:-/etc/tieudidi/nexus-ci.password}
PUBLIC_URL=https://gtb.nhaboc.com
NS=gtb
POD=gtb-server-0

step() { printf '\n==> %s\n' "$*"; }

if [ -n "${GTB_ADMIN_TOKEN:-}" ] && [ "${#GTB_ADMIN_TOKEN}" -lt 32 ]; then
  echo "GTB_ADMIN_TOKEN must be at least 32 characters (the server refuses to start otherwise)" >&2
  exit 1
fi

step "Source: $REPO_URL -> $SRC"
if [ -d "$SRC/.git" ]; then
  git -C "$SRC" fetch -q origin && git -C "$SRC" checkout -q main && git -C "$SRC" reset -q --hard origin/main
else
  git clone -q "$REPO_URL" "$SRC"
fi
TAG=$(git -C "$SRC" rev-parse --short HEAD)
VERSION=$(git -C "$SRC" describe --tags --always 2>/dev/null || echo "$TAG")
IMAGE="$REGISTRY/gtb:$TAG"

step "Host volume /var/lib/gtb/server (uid 1000)"
sudo mkdir -p /var/lib/gtb/server
sudo chown 1000:1000 /var/lib/gtb/server
sudo chmod 0700 /var/lib/gtb/server

step "Build $IMAGE"
docker build --platform linux/amd64 -f "$SRC/deploy/Dockerfile.server" \
  --build-arg VERSION="$VERSION" --build-arg COMMIT="$TAG" \
  -t "$IMAGE" -t "$REGISTRY/gtb:latest" "$SRC"

step "Push to Nexus ($REGISTRY, user $NEXUS_USER)"
sudo cat "$NEXUS_PASSWORD_FILE" | docker login "$REGISTRY" -u "$NEXUS_USER" --password-stdin >/dev/null
docker push -q "$IMAGE"
docker push -q "$REGISTRY/gtb:latest"

step "Namespace and Secret gtb-server"
kubectl get ns "$NS" >/dev/null 2>&1 || kubectl create ns "$NS"
if [ -n "${GTB_ADMIN_TOKEN:-}" ]; then
  # The token goes through stdin only: never on a command line or in output.
  printf '%s' "$GTB_ADMIN_TOKEN" \
    | kubectl -n "$NS" create secret generic gtb-server --from-file=GTB_ADMIN_TOKEN=/dev/stdin \
        --dry-run=client -o yaml \
    | kubectl apply -f - >/dev/null
  echo "Secret gtb-server: GTB_ADMIN_TOKEN set from the environment"
elif kubectl -n "$NS" get secret gtb-server >/dev/null 2>&1; then
  echo "Secret gtb-server exists; left unchanged"
else
  # The env ref is optional: an empty Secret means "server generates a token".
  kubectl -n "$NS" create secret generic gtb-server >/dev/null
  echo "Secret gtb-server created empty (the server generates the admin token on first start)"
fi

step "Apply manifests"
had_sts=$(kubectl -n "$NS" get sts gtb-server -o name 2>/dev/null || true)
sed "s|IMAGE_TAG|$TAG|" "$SRC/deploy/k3s/gtb.yaml" | kubectl apply -f -
# Env from a Secret is read at container start only: restart so a token
# passed on a redeploy of the same commit takes effect.
if [ -n "${GTB_ADMIN_TOKEN:-}" ] && [ -n "$had_sts" ]; then
  kubectl -n "$NS" rollout restart sts/gtb-server
fi
# apply has already pointed Service gtb at app: gtb-server, so a failed
# rollout leaves gtb.nhaboc.com with no backend: say how to go back.
if ! kubectl -n "$NS" rollout status sts/gtb-server --timeout=5m; then
  echo "ROLLOUT FAILED: gtb.nhaboc.com has no backend. To go back to the old Gas Town pod:" >&2
  if kubectl -n "$NS" get sts gtb >/dev/null 2>&1; then
    echo "  kubectl -n $NS patch svc gtb -p '{\"spec\":{\"selector\":{\"app\":\"gtb\"}}}'" >&2
  else
    echo "  (none: the old StatefulSet gtb no longer exists)" >&2
  fi
  echo "Inspect: kubectl -n $NS describe pod $POD; kubectl -n $NS logs $POD" >&2
  exit 1
fi

step "Admin token"
# The server prints the token frame once, on the first start of an empty
# store. GTB_ADMIN_TOKEN from the Secret suppresses it.
frame=$(kubectl -n "$NS" logs "$POD" 2>/dev/null | sed -n '/dashboard admin token/,/^====/p' || true)
if [ -n "$frame" ]; then
  printf '%s\n' "$frame"
  echo "This is shown once. Store it now."
elif [ -n "${GTB_ADMIN_TOKEN:-}" ]; then
  echo "The admin token is the GTB_ADMIN_TOKEN you passed in."
elif [ -n "$(kubectl -n "$NS" get secret gtb-server -o jsonpath='{.data.GTB_ADMIN_TOKEN}' 2>/dev/null || true)" ]; then
  # Only tested for non-empty; never printed. The server adopts this env
  # token on every start, so an in-pod --rotate would be undone.
  echo "No token frame in the log: the token is managed by Secret gtb-server."
  echo "To change it, re-run this script with GTB_ADMIN_TOKEN=<new> (or edit the Secret), then:"
  echo "  kubectl -n $NS rollout restart sts/gtb-server"
else
  echo "No token frame in the log: an admin token already exists."
  echo "To replace it (revokes all sessions; the new token is printed once):"
  echo "  kubectl -n $NS exec $POD -- gtb server admin-token --rotate --data /data --public-url $PUBLIC_URL"
fi

step "Edge: gtb.nhaboc.com -> 127.0.0.1:30881"
# Clone the gastown vhost the first time; nginx config is host-owned, so
# anything unexpected is reported rather than guessed.
src_conf=$(sudo grep -rl 'gastown.nhaboc.com' /etc/nginx 2>/dev/null | head -1 || true)
if sudo grep -rqs 'gtb.nhaboc.com' /etc/nginx; then
  echo "nginx already serves gtb.nhaboc.com"
elif [ -n "$src_conf" ] && [ "$(sudo grep -c server_name "$src_conf")" -eq 1 ]; then
  dst_conf=$(dirname "$src_conf")/gtb.conf
  sudo sed -e 's/gastown\.nhaboc\.com/gtb.nhaboc.com/g' -e 's/30880/30881/g' "$src_conf" | sudo tee "$dst_conf" >/dev/null
  [ -d /etc/nginx/sites-enabled ] && [ "$(dirname "$dst_conf")" = /etc/nginx/sites-available ] \
    && sudo ln -sf "$dst_conf" /etc/nginx/sites-enabled/gtb.conf
  sudo nginx -t && sudo systemctl reload nginx
  echo "wrote $dst_conf from $src_conf"
else
  echo "MANUAL: no single-vhost nginx file for gastown.nhaboc.com found (got: ${src_conf:-none})."
  echo "        Add gtb.nhaboc.com -> 127.0.0.1:30881 next to the gastown entry, and 30881 to TERMINATOR_PORTS if nginx reaches NodePorts via the firewall."
fi
# The server trusts X-Forwarded-For only from GTB_TRUSTED_PROXIES; without
# the header every login attempt looks like it comes from the proxy.
gtb_conf=$(sudo grep -rl 'gtb.nhaboc.com' /etc/nginx 2>/dev/null | head -1 || true)
if [ -n "$gtb_conf" ] && sudo grep -Eqs 'proxy_set_header[[:space:]]+X-Forwarded-For' "$gtb_conf"; then
  echo "nginx ($gtb_conf) sets X-Forwarded-For"
else
  echo "MANUAL: the nginx vhost for gtb.nhaboc.com (${gtb_conf:-not found}) does not set X-Forwarded-For."
  echo "        Add this line inside its 'location /' block, next to proxy_pass, then run: sudo nginx -t && sudo systemctl reload nginx"
  echo "          proxy_set_header X-Forwarded-For \$proxy_add_x_forwarded_for;"
fi

echo
echo "Deployed $IMAGE -> $PUBLIC_URL"
echo "Log in at $PUBLIC_URL/login (DNS: add a Cloudflare DNS-only A record gtb -> 100.76.86.103 if missing)"

cat <<OLD

Old Gas Town objects (delete when you are sure):
  kubectl -n $NS delete statefulset gtb gtb-dolt
  kubectl -n $NS delete service gtb-dolt
  kubectl -n $NS delete configmap gtb-dolt-config
  kubectl -n $NS delete pvc data-gtb-0 data-gtb-dolt-0
  kubectl delete pv gtb-data gtb-dolt-data
  kubectl -n $NS delete networkpolicy allow-dolt allow-gtb
  kubectl -n $NS delete secret gtb-git gtb-claude-env
Then, by hand on beboc, everything under /var/lib/gtb except server/ can be removed
(the PVs are Retain, so deleting them leaves the files):
  sudo find /var/lib/gtb -mindepth 1 -maxdepth 1 ! -name server -exec rm -rf {} +
OLD
