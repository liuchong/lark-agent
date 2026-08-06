#!/usr/bin/env bash
set -euo pipefail

if [ "$#" -ne 1 ]; then
  echo "usage: update-private-env.sh ENV_FILE" >&2
  exit 2
fi

env_file="$1"
env_dir="$(dirname "$env_file")"
mkdir -p "$env_dir"

candidate="$(mktemp "$env_dir/.env.candidate.XXXXXX")"
filtered="$candidate.filtered"
cleanup() {
  rm -f "$candidate" "$filtered"
}
trap cleanup EXIT

if [ -f "$env_file" ]; then
  cp "$env_file" "$candidate"
else
  : > "$candidate"
fi

update_key() {
  local key="$1"
  local present="$2"
  local value="$3"
  if [ "$present" -ne 1 ]; then
    return
  fi

  awk -v key="$key" '
    {
      candidate = $0
      sub(/^[[:space:]]*export[[:space:]]+/, "", candidate)
      if (index(candidate, key "=") != 1) {
        print $0
      }
    }
  ' "$candidate" > "$filtered"
  mv -f "$filtered" "$candidate"

  if [ -n "$value" ]; then
    printf '%s=%q\n' "$key" "$value" >> "$candidate"
  fi
}

api_key_present=0
base_url_present=0
model_present=0
[ "${OPENAI_API_KEY+x}" = x ] && api_key_present=1
[ "${OPENAI_BASE_URL+x}" = x ] && base_url_present=1
[ "${OPENAI_MODEL+x}" = x ] && model_present=1

update_key "OPENAI_API_KEY" "$api_key_present" "${OPENAI_API_KEY:-}"
update_key "OPENAI_BASE_URL" "$base_url_present" "${OPENAI_BASE_URL:-}"
update_key "OPENAI_MODEL" "$model_present" "${OPENAI_MODEL:-}"

chmod 600 "$candidate"
mv -f "$candidate" "$env_file"
chmod 600 "$env_file"
