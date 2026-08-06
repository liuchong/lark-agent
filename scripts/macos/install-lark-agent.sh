#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
APP_SUPPORT="$HOME/Library/Application Support/lark-agent"
BIN_DIR="$APP_SUPPORT/bin"
ENV_FILE="$APP_SUPPORT/env"
CONF_FILE="$APP_SUPPORT/agent.conf"
WRAPPER="$BIN_DIR/lark-agent-daemon"
AGENT_BIN="$BIN_DIR/lark-agent"
AGENT_CANDIDATE="$BIN_DIR/.lark-agent.candidate.$$"
INSTALL_LOCK="$APP_SUPPORT/.install.lock"
INSTALL_BACKUP="$APP_SUPPORT/.install-backup.$$"
APP_DIR="$HOME/Applications/Lark Agent.app"
APP_CONTENTS="$APP_DIR/Contents"
APP_MACOS="$APP_CONTENTS/MacOS"
APP_RESOURCES="$APP_CONTENTS/Resources"
INFO_PLIST="$APP_CONTENTS/Info.plist"
APP_ICON="$APP_RESOURCES/LarkAgent.icns"
STATUS_ICON="$APP_RESOURCES/StatusIconTemplate.png"
STATUS_APP="$APP_MACOS/LarkAgentStatus"
STATUS_CANDIDATE="$APP_MACOS/.LarkAgentStatus.candidate.$$"
LAUNCH_AGENT_PLIST="$HOME/Library/LaunchAgents/com.liuchong.lark-agent.plist"
CONFIG_PATH="${CONFIG_PATH:-$HOME/.config/lark-agent/config.yaml}"
STATE_PATH="${STATE_PATH:-$APP_SUPPORT/state.db}"
CHAT_QUERY="${CHAT_QUERY:-Test Group}"
POLL_INTERVAL="${POLL_INTERVAL:-10s}"
INSTALL_LOAD="${INSTALL_LOAD:-1}"
OPEN_STATUS_APP="${OPEN_STATUS_APP:-1}"
MODEL_MIGRATION_DOCTOR="${MODEL_MIGRATION_DOCTOR:-1}"
LABEL="com.liuchong.lark-agent"
lock_acquired=0
backup_prepared=0
service_stopped=0
service_was_loaded=0
install_succeeded=0
model_keychain_modified=0
model_keychain_had_old=0
model_keychain_old_value=""
model_keychain_service="lark-agent"
model_keychain_account="model/primary/api-key"
backup_targets=()
backup_copies=()

backup_installation() {
  mkdir -p "$INSTALL_BACKUP"
  backup_prepared=1
  local target copy index
  for target in \
    "$AGENT_BIN" \
    "$WRAPPER" \
    "$CONF_FILE" \
    "$ENV_FILE" \
    "$INFO_PLIST" \
    "$APP_ICON" \
    "$STATUS_ICON" \
    "$STATUS_APP" \
    "$LAUNCH_AGENT_PLIST" \
    "$CONFIG_PATH" \
    "$STATE_PATH" \
    "$STATE_PATH-wal" \
    "$STATE_PATH-shm"; do
    index="${#backup_targets[@]}"
    copy=""
    if [ -e "$target" ]; then
      copy="$INSTALL_BACKUP/$index"
      cp -p "$target" "$copy"
    fi
    backup_targets+=("$target")
    backup_copies+=("$copy")
  done
}

restore_installation() {
  local i target copy failed=0
  for ((i = ${#backup_targets[@]} - 1; i >= 0; i--)); do
    target="${backup_targets[$i]}"
    copy="${backup_copies[$i]}"
    if ! rm -f "$target"; then
      echo "Failed to remove partially installed artifact during rollback: $target" >&2
      failed=1
      continue
    fi
    if [ -n "$copy" ]; then
      if ! mkdir -p "$(dirname "$target")" || ! cp -p "$copy" "$target"; then
        echo "Failed to restore installation artifact: $target" >&2
        failed=1
      fi
    fi
  done
  return "$failed"
}

wait_for_label_unloaded() {
  local attempt
  for ((attempt = 0; attempt < 100; attempt++)); do
    if ! launchctl print "gui/$UID/$LABEL" >/dev/null 2>&1; then
      return 0
    fi
    sleep 0.1
  done
  return 1
}

cleanup() {
  local exit_code=$?
  local rollback_failed=0
  set +e
  rm -f "$AGENT_CANDIDATE" "$STATUS_CANDIDATE"
  if [ "$install_succeeded" -ne 1 ] && [ "$model_keychain_modified" -eq 1 ]; then
    if [ "$model_keychain_had_old" -eq 1 ]; then
      if ! security add-generic-password -U \
        -s "$model_keychain_service" \
        -a "$model_keychain_account" \
        -w "$model_keychain_old_value" >/dev/null 2>&1; then
        echo "Failed to restore previous model API key in Keychain." >&2
        rollback_failed=1
      fi
    else
      security delete-generic-password \
        -s "$model_keychain_service" \
        -a "$model_keychain_account" >/dev/null 2>&1 || true
    fi
  fi
  if [ "$install_succeeded" -ne 1 ] && [ "$backup_prepared" -eq 1 ]; then
    if ! restore_installation; then
      rollback_failed=1
    fi
  fi
  if [ "$install_succeeded" -ne 1 ] &&
    [ "$service_was_loaded" -eq 1 ] &&
    [ "$service_stopped" -eq 1 ] &&
    ! launchctl print "gui/$UID/$LABEL" >/dev/null 2>&1; then
    if [ -f "$LAUNCH_AGENT_PLIST" ]; then
      if ! launchctl bootstrap "gui/$UID" "$LAUNCH_AGENT_PLIST" >/dev/null 2>&1; then
        echo "Failed to restore the previously loaded lark-agent service." >&2
        rollback_failed=1
      fi
    else
      echo "Failed to restore the previous service because its plist is unavailable." >&2
      rollback_failed=1
    fi
  fi
  if ! rm -rf "$INSTALL_BACKUP"; then
    echo "Failed to remove the private installation backup: $INSTALL_BACKUP" >&2
    rollback_failed=1
  fi
  if [ "$lock_acquired" -eq 1 ]; then
    if ! rm -rf "$INSTALL_LOCK"; then
      echo "Failed to release the installation lock: $INSTALL_LOCK" >&2
      rollback_failed=1
    fi
  fi
  if [ "$rollback_failed" -ne 0 ] && [ "$exit_code" -eq 0 ]; then
    exit_code=1
  fi
  return "$exit_code"
}
trap cleanup EXIT

read_private_env_value() {
  local key="$1"
  if [ ! -f "$ENV_FILE" ]; then
    return 0
  fi
  env -i HOME="$HOME" bash -c '
    set -a
    # shellcheck disable=SC1090
    source "$1"
    set +a
    printf "%s" "${!2:-}"
  ' _ "$ENV_FILE" "$key"
}

migrate_legacy_model_env() {
  local env_api_key env_base_url env_model api_key base_url model profile_args=()
  if [ "${OPENAI_API_KEY+x}" = x ]; then
    api_key="${OPENAI_API_KEY:-}"
  else
    env_api_key="$(read_private_env_value OPENAI_API_KEY)"
    api_key="$env_api_key"
  fi
  if [ "${OPENAI_BASE_URL+x}" = x ]; then
    base_url="${OPENAI_BASE_URL:-}"
  else
    env_base_url="$(read_private_env_value OPENAI_BASE_URL)"
    base_url="$env_base_url"
  fi
  if [ "${OPENAI_MODEL+x}" = x ]; then
    model="${OPENAI_MODEL:-}"
  else
    env_model="$(read_private_env_value OPENAI_MODEL)"
    model="$env_model"
  fi

  if [ -n "$base_url" ] || [ -n "$model" ]; then
    profile_args=(model profile set primary --provider kimi --protocol openai_chat)
    if [ -n "$base_url" ]; then
      profile_args+=(--base-url "$base_url")
    fi
    if [ -n "$model" ]; then
      profile_args+=(--model "$model")
    fi
    "$AGENT_CANDIDATE" --config "$CONFIG_PATH" "${profile_args[@]}" >/dev/null
  fi

  if [ -n "$api_key" ]; then
    model_keychain_old_value="$(security find-generic-password \
      -w \
      -s "$model_keychain_service" \
      -a "$model_keychain_account" 2>/dev/null || true)"
    if [ -n "$model_keychain_old_value" ]; then
      model_keychain_had_old=1
    fi
    security add-generic-password -U \
      -s "$model_keychain_service" \
      -a "$model_keychain_account" \
      -w "$api_key" >/dev/null
    model_keychain_modified=1
    if [ "$MODEL_MIGRATION_DOCTOR" = "1" ]; then
      "$AGENT_CANDIDATE" --config "$CONFIG_PATH" model doctor primary >/dev/null
    fi
    OPENAI_API_KEY= OPENAI_BASE_URL= OPENAI_MODEL= \
      bash "$ROOT/scripts/macos/update-private-env.sh" "$ENV_FILE"
  elif [ -n "$base_url" ] || [ -n "$model" ]; then
    "$AGENT_CANDIDATE" --config "$CONFIG_PATH" model auth status primary >/dev/null
  fi
}

mkdir -p \
  "$BIN_DIR" \
  "$APP_MACOS" \
  "$APP_RESOURCES" \
  "$HOME/Library/Logs/lark-agent" \
  "$(dirname "$CONFIG_PATH")" \
  "$(dirname "$STATE_PATH")"

if ! mkdir "$INSTALL_LOCK" 2>/dev/null; then
  echo "Another lark-agent installation is already in progress: $INSTALL_LOCK" >&2
  exit 1
fi
lock_acquired=1

echo "Building standalone lark-agent..."
(cd "$ROOT" && go build -trimpath -o "$AGENT_CANDIDATE" ./cmd/lark-agent)

if [ ! -f "$CONFIG_PATH" ]; then
  echo "No lark-agent config was found. Run lark-agent init and lark-agent auth login before installation." >&2
  exit 1
fi

echo "Building menu bar status app candidate..."
swiftc "$ROOT/macos/LarkAgentStatus/main.swift" -framework AppKit -o "$STATUS_CANDIDATE"
for asset in \
  "$ROOT/assets/brand/LarkAgent.icns" \
  "$ROOT/assets/brand/lark-agent-status-template.png"; do
  if [ ! -f "$asset" ]; then
    echo "Required brand asset is missing: $asset" >&2
    exit 1
  fi
done

if launchctl print "gui/$UID/$LABEL" >/dev/null 2>&1; then
  service_was_loaded=1
  if [ ! -x "$WRAPPER" ]; then
    echo "The standalone LaunchAgent is loaded but its installed wrapper is unavailable." >&2
    exit 1
  fi
  echo "Stopping existing standalone lark-agent before replacement..."
  "$WRAPPER" --config "$CONFIG_PATH" --state "$STATE_PATH" daemon stop
  service_stopped=1
  if ! wait_for_label_unloaded; then
    echo "The standalone LaunchAgent remained loaded; refusing to replace its executable." >&2
    exit 1
  fi
fi

backup_installation
rm -f "$LAUNCH_AGENT_PLIST"
if launchctl print "gui/$UID/$LABEL" >/dev/null 2>&1; then
  echo "The standalone LaunchAgent was loaded again during upgrade; refusing to replace its files." >&2
  exit 1
fi

echo "Migrating model profile and Keychain configuration..."
migrate_legacy_model_env

echo "Running full readiness doctor against the candidate..."
"$AGENT_CANDIDATE" \
  --config "$CONFIG_PATH" \
  --state "$STATE_PATH" \
  doctor

mv -f "$AGENT_CANDIDATE" "$AGENT_BIN"

cat > "$WRAPPER" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
unset LARKSUITE_CLI_CONFIG_DIR
unset LARKSUITE_CLI_NO_UPDATE_NOTIFIER
unset LARKSUITE_CLI_NO_SKILLS_NOTIFIER
ENV_FILE="$HOME/Library/Application Support/lark-agent/env"
if [ -f "$ENV_FILE" ]; then
  set -a
  # shellcheck disable=SC1090
  source "$ENV_FILE"
  set +a
fi
exec "$HOME/Library/Application Support/lark-agent/bin/lark-agent" "$@"
EOF
chmod 700 "$WRAPPER"

{
  printf 'CONFIG_PATH=%s\n' "$CONFIG_PATH"
  printf 'STATE_PATH=%s\n' "$STATE_PATH"
} > "$CONF_FILE"
chmod 600 "$CONF_FILE"

bash "$ROOT/scripts/macos/update-private-env.sh" "$ENV_FILE"

cat > "$INFO_PLIST" <<'EOF'
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>CFBundleExecutable</key>
  <string>LarkAgentStatus</string>
  <key>CFBundleIdentifier</key>
  <string>com.liuchong.lark-agent.status</string>
  <key>CFBundleName</key>
  <string>Lark Agent</string>
  <key>CFBundleIconFile</key>
  <string>LarkAgent</string>
  <key>CFBundlePackageType</key>
  <string>APPL</string>
  <key>LSUIElement</key>
  <true/>
</dict>
</plist>
EOF

install -m 644 "$ROOT/assets/brand/LarkAgent.icns" "$APP_ICON"
install -m 644 "$ROOT/assets/brand/lark-agent-status-template.png" "$STATUS_ICON"
mv -f "$STATUS_CANDIDATE" "$STATUS_APP"

install_args=(
  --config "$CONFIG_PATH"
  --state "$STATE_PATH"
  daemon install-app
  --program "$WRAPPER"
  --chat-query "$CHAT_QUERY"
  --poll-interval "$POLL_INTERVAL"
  --write
)
if [ "$INSTALL_LOAD" = "1" ]; then
  install_args+=(--load)
fi
echo "Installing standalone LaunchAgent..."
"$WRAPPER" "${install_args[@]}"
install_succeeded=1
rm -rf "$INSTALL_BACKUP"

if [ "$OPEN_STATUS_APP" = "1" ]; then
  open "$APP_DIR"
fi

echo "Installed standalone lark-agent:"
echo "  App: $APP_DIR"
echo "  Agent: $AGENT_BIN"
echo "  Config: $CONFIG_PATH"
echo "  State: $STATE_PATH"
echo "  Logs: $HOME/Library/Logs/lark-agent"
