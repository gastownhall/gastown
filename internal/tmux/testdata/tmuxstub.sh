#!/bin/sh
# Test stub that wraps the real tmux binary. Controlled by env:
#   GT_REAL_TMUX              required; path to the real tmux
#   GT_TMUX_ARGV_LOG          if set, append each invocation's args
#   GT_TMUX_FAIL_SEND_KEYS    if 1, every send-keys fails like tmux 3.7b unknown -V
#   GT_TMUX_REJECT_37B_FLAGS  if 1, send-keys -V / -o are rejected (tmux 3.7b)
#   GT_TMUX_NOOP_NAMED_ENTER  if 1, named-key Enter/C-m/KPEnter succeed but do nothing
set -eu

if [ -n "${GT_TMUX_ARGV_LOG:-}" ]; then
	printf '%s\n' "$*" >>"$GT_TMUX_ARGV_LOG"
fi

is_send_keys=0
for arg in "$@"; do
	if [ "$arg" = "send-keys" ]; then
		is_send_keys=1
		break
	fi
done

if [ "$is_send_keys" = 1 ]; then
	if [ "${GT_TMUX_FAIL_SEND_KEYS:-}" = "1" ]; then
		echo "command send-keys: unknown flag -V" >&2
		exit 1
	fi
	if [ "${GT_TMUX_REJECT_37B_FLAGS:-}" = "1" ]; then
		for arg in "$@"; do
			if [ "$arg" = "-V" ]; then
				echo "command send-keys: unknown flag -V" >&2
				exit 1
			fi
			if [ "$arg" = "-o" ]; then
				echo "command send-keys: unknown flag -o" >&2
				exit 1
			fi
		done
	fi
	if [ "${GT_TMUX_NOOP_NAMED_ENTER:-}" = "1" ]; then
		literal=0
		named=0
		for arg in "$@"; do
			if [ "$arg" = "-l" ]; then
				literal=1
			fi
			case "$arg" in
			Enter | C-m | KPEnter)
				named=1
				;;
			esac
		done
		if [ "$named" = 1 ] && [ "$literal" = 0 ]; then
			exit 0
		fi
	fi
fi

if [ -z "${GT_REAL_TMUX:-}" ]; then
	echo "tmuxstub: GT_REAL_TMUX is unset" >&2
	exit 127
fi
exec "$GT_REAL_TMUX" "$@"
