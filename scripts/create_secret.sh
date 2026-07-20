#!/usr/bin/env sh
set -eu

NAMESPACE=${NAMESPACE:-venator}
SECRET_NAME=${SECRET_NAME:-venator-secrets}
ENV_FILE=${ENV_FILE:-.vcfg.env}

if [ ! -f "${ENV_FILE}" ]; then
  printf 'Missing %s. Copy scripts/dot_vcfg.env and replace its example values.\n' "${ENV_FILE}" >&2
  exit 1
fi

kubectl create namespace "${NAMESPACE}" --dry-run=client -o yaml | kubectl apply -f -
kubectl create secret generic "${SECRET_NAME}" \
  --namespace "${NAMESPACE}" \
  --from-env-file="${ENV_FILE}" \
  --dry-run=client -o yaml | kubectl apply -f -
