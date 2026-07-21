#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
RELAY_DIR="${ROOT_DIR}/application/relay"
RELAY_ENV_FILE="${RELAY_DIR}/.env"

read_env_value() {
  local key="$1"
  local file="$2"
  if [[ ! -f "${file}" ]]; then
    return 0
  fi
  awk -F= -v k="${key}" '
    $0 ~ "^[[:space:]]*"k"=" {
      val = substr($0, index($0, "=") + 1)
      gsub(/^[[:space:]]+|[[:space:]]+$/, "", val)
      gsub(/^"|"$/, "", val)
      print val
      exit
    }
  ' "${file}"
}

resolve_app_dir() {
  local raw="${APP_DIR:-}"
  if [[ -z "${raw}" ]]; then
    raw="$(read_env_value "APP_DIR" "${RELAY_ENV_FILE}")"
  fi
  if [[ -z "${raw}" ]]; then
    raw="${RELAY_DIR}/.hexvision_data"
  fi
  if [[ "${raw}" == /* ]]; then
    echo "${raw}"
  else
    local base="${RELAY_DIR}"
    if [[ "${raw}" == .* ]]; then
      echo "$(cd "${base}" && realpath -m "${raw}")"
    else
      echo "$(cd "${base}" && realpath -m "./${raw}")"
    fi
  fi
}

APP_DIR_RESOLVED="$(resolve_app_dir)"
RUN_DIR="${APP_DIR_RESOLVED}/run"
LOG_DIR="${APP_DIR_RESOLVED}/logs"
LOCK_FILE="${RUN_DIR}/relay.lock"
PID_FILE="${RUN_DIR}/relay.pid"
LOG_FILE="${LOG_DIR}/relay.log"
RELAY_PORT="${RELAY_PORT:-$(read_env_value "RELAY_PORT" "${RELAY_ENV_FILE}")}"
API_PORT="${API_PORT:-$(read_env_value "API_PORT" "${RELAY_ENV_FILE}")}"

pid_from_metadata() {
  if [[ ! -f "${PID_FILE}" ]]; then
    return 0
  fi
  sed -n 's/.*"pid"[[:space:]]*:[[:space:]]*\([0-9][0-9]*\).*/\1/p' "${PID_FILE}" | head -n1
}

pid_running() {
  local pid="$1"
  [[ -n "${pid}" ]] || return 1
  kill -0 "${pid}" >/dev/null 2>&1
}

relay_running_pid() {
  local pid
  pid="$(pid_from_metadata || true)"
  if pid_running "${pid}"; then
    echo "${pid}"
    return 0
  fi
  return 1
}

print_children() {
  local pid="$1"
  ps -eo pid,ppid,cmd | awk -v p="${pid}" '$2 == p {print "  " $0}'
}

cmd_start() {
  local detach="false"
  local launcher_tag="${VYRA_LAUNCHER_TAG:-vyractl}"
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --detach)
        detach="true"
        shift
        ;;
      --launcher-tag)
        launcher_tag="${2:-}"
        shift 2
        ;;
      *)
        echo "Unknown start option: $1" >&2
        exit 1
        ;;
    esac
  done

  mkdir -p "${RUN_DIR}" "${LOG_DIR}"

  if pid="$(relay_running_pid)"; then
    echo "Relay already running (pid ${pid}). Use 'scripts/vyractl.sh status' or 'scripts/vyractl.sh stop'." >&2
    exit 1
  fi

  if [[ "${detach}" == "true" ]]; then
    (
      cd "${RELAY_DIR}"
      VYRA_LAUNCHER_TAG="${launcher_tag}" nohup go run ./cmd/relay >>"${LOG_FILE}" 2>&1 &
    )
    sleep 1
    if pid="$(relay_running_pid)"; then
      echo "Relay started in detached mode (pid ${pid})."
      echo "Logs: ${LOG_FILE}"
      return 0
    fi
    echo "Relay failed to start. Check logs: ${LOG_FILE}" >&2
    return 1
  fi

  cd "${RELAY_DIR}"
  VYRA_LAUNCHER_TAG="${launcher_tag}" go run ./cmd/relay
}

cmd_stop() {
  local pid=""
  if pid="$(relay_running_pid)"; then
    :
  else
    pid="$(pgrep -f "${RELAY_DIR}.*cmd/relay|go run ./cmd/relay" | head -n1 || true)"
  fi

  if [[ -z "${pid}" ]]; then
    echo "Relay is not running."
    rm -f "${PID_FILE}" "${LOCK_FILE}"
    return 0
  fi

  echo "Stopping relay (pid ${pid})..."
  kill -TERM "${pid}" >/dev/null 2>&1 || true
  for _ in $(seq 1 20); do
    if ! pid_running "${pid}"; then
      echo "Relay stopped."
      rm -f "${PID_FILE}" "${LOCK_FILE}"
      return 0
    fi
    sleep 0.5
  done

  echo "Relay did not stop in time. Sending SIGKILL to ${pid}."
  kill -KILL "${pid}" >/dev/null 2>&1 || true
  rm -f "${PID_FILE}" "${LOCK_FILE}"
}

cmd_status() {
  if pid="$(relay_running_pid)"; then
    echo "Relay: running (pid ${pid})"
  else
    echo "Relay: stopped"
    if [[ -f "${PID_FILE}" ]]; then
      echo "  stale pid metadata: ${PID_FILE}"
    fi
    return 0
  fi

  if [[ -n "${API_PORT}" || -n "${RELAY_PORT}" ]]; then
    echo "Ports:"
    [[ -n "${API_PORT}" ]] && echo "  API:   ${API_PORT}"
    [[ -n "${RELAY_PORT}" ]] && echo "  Relay: ${RELAY_PORT}"
  fi

  echo "Managed child inventory:"
  local children
  children="$(print_children "${pid}" || true)"
  if [[ -n "${children}" ]]; then
    echo "${children}"
  else
    echo "  (none)"
  fi
}

cmd_logs() {
  local lines="${TAIL_LINES:-200}"
  mkdir -p "${LOG_DIR}"
  touch "${LOG_FILE}"
  echo "Tailing ${LOG_FILE} (last ${lines} lines)"
  tail -n "${lines}" -f "${LOG_FILE}"
}

usage() {
  cat <<'EOF'
Usage: scripts/vyractl.sh <command> [options]

Commands:
  start [--detach] [--launcher-tag <tag>]
  stop
  status
  logs
EOF
}

main() {
  local cmd="${1:-}"
  if [[ -z "${cmd}" ]]; then
    usage
    exit 1
  fi
  shift || true

  case "${cmd}" in
    start)  cmd_start "$@" ;;
    stop)   cmd_stop "$@" ;;
    status) cmd_status "$@" ;;
    logs)   cmd_logs "$@" ;;
    -h|--help|help)
      usage
      ;;
    *)
      echo "Unknown command: ${cmd}" >&2
      usage
      exit 1
      ;;
  esac
}

main "$@"
