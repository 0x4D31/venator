#!/bin/sh
set -eu
umask 077

CONFIG_HOME=${XDG_CONFIG_HOME:-"${HOME}/.config"}/venator
ENV_FILE=${CONFIG_HOME}/venator.env

if [ -f "${ENV_FILE}" ]; then
  set -a
  # This file is trusted deployment configuration and must be mode 0600.
  . "${ENV_FILE}"
  set +a
fi

exec "${HOME}/.local/bin/venator" \
  run \
  --global-config "${CONFIG_HOME}/global.yaml" \
  --rule-config "${CONFIG_HOME}/rules/example.yaml"
