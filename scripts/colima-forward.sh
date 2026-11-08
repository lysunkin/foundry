#!/usr/bin/env bash
# Make a registry running on the macOS host reachable as localhost:<port> from
# the Docker daemon inside a colima (Lima) VM.
#
# Why: with colima, `docker push localhost:8080/...` is executed by dockerd
# inside the VM, where localhost is the VM itself. The host is reachable as
# host.lima.internal, but using that name would require an insecure-registries
# entry and a colima restart.
#
# How: an iptables DNAT rule in the VM rewrites outbound connections to
# 127.0.0.1:<port> to <host.lima.internal>:<port>. No socket is opened in the
# VM, so Lima's automatic port forwarding (which would bind the host's
# 127.0.0.1:<port> and create a loop) is not triggered. dockerd already treats
# 127.0.0.0/8 as an insecure (plain-HTTP) registry.
#
# The rules live in the VM's kernel and are lost when colima restarts; re-run
# `up`. Requires net.ipv4.conf.all.route_localnet=1 in the VM (set by `up`,
# restored to 0 by `down`).
#
# Usage: scripts/colima-forward.sh up|down|status [port]   (default port 8080)
set -euo pipefail

cmd="${1:-status}"
port="${2:-8080}"
profile="${COLIMA_PROFILE:-default}"

if ! command -v colima >/dev/null 2>&1; then
	echo "colima not found — this script is only needed for colima-based Docker." >&2
	exit 1
fi
if ! colima status -p "$profile" >/dev/null 2>&1; then
	echo "colima profile '$profile' is not running." >&2
	exit 1
fi

colima ssh -p "$profile" -- sudo bash -s -- "$cmd" "$port" <<'VM'
set -euo pipefail
cmd="$1"; port="$2"
tag="foundry-registry-forward-$port"
host_ip="$(getent ahostsv4 host.lima.internal | awk 'NR==1{print $1}')"
[ -n "$host_ip" ] || { echo "cannot resolve host.lima.internal" >&2; exit 1; }

dnat=(OUTPUT -p tcp -d 127.0.0.1 --dport "$port" -m comment --comment "$tag" -j DNAT --to-destination "$host_ip:$port")
masq=(POSTROUTING -p tcp -s 127.0.0.1 -d "$host_ip" --dport "$port" -m comment --comment "$tag" -j MASQUERADE)

has() { iptables -t nat -C "$@" 2>/dev/null; }

case "$cmd" in
up)
	sysctl -qw net.ipv4.conf.all.route_localnet=1
	has "${dnat[@]}" || iptables -t nat -I "${dnat[@]}"
	has "${masq[@]}" || iptables -t nat -I "${masq[@]}"
	echo "forward up: VM localhost:$port -> host $host_ip:$port"
	;;
down)
	while has "${dnat[@]}"; do iptables -t nat -D "${dnat[@]}"; done
	while has "${masq[@]}"; do iptables -t nat -D "${masq[@]}"; done
	# Only restore route_localnet if no other forward of ours remains.
	if ! iptables -t nat -S | grep -q "foundry-registry-forward-"; then
		sysctl -qw net.ipv4.conf.all.route_localnet=0
	fi
	echo "forward down (port $port)"
	;;
status)
	if has "${dnat[@]}" && has "${masq[@]}"; then
		echo "forward up: VM localhost:$port -> host $host_ip:$port"
	else
		echo "forward down (port $port)"
	fi
	;;
*)
	echo "usage: colima-forward.sh up|down|status [port]" >&2; exit 2 ;;
esac
VM
