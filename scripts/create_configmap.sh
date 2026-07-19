#!/usr/bin/env sh
set -eu

NAMESPACE=${NAMESPACE:-venator}
GLOBAL_CONFIG=${GLOBAL_CONFIG:-deploy/kubernetes/global.yaml}
GLOBAL_CONFIGMAP_NAME=${GLOBAL_CONFIGMAP_NAME:-venator-global-config}
RULE_CONFIG=${RULE_CONFIG:-deploy/kubernetes/rule.yaml}
RULE_CONFIGMAP_NAME=${RULE_CONFIGMAP_NAME:-venator-rule}
RULE_DATA_FILE=${RULE_DATA_FILE-deploy/kubernetes/events.ndjson}
RULE_DATA_KEY=${RULE_DATA_KEY:-events.ndjson}
EXCLUSIONS_CONFIG=${EXCLUSIONS_CONFIG:-}
EXCLUSIONS_CONFIGMAP_NAME=${EXCLUSIONS_CONFIGMAP_NAME:-venator-exclusions}

kubectl create namespace "${NAMESPACE}" --dry-run=client -o yaml | kubectl apply -f -

kubectl create configmap "${GLOBAL_CONFIGMAP_NAME}" \
  --namespace "${NAMESPACE}" \
  --from-file=global.yaml="${GLOBAL_CONFIG}" \
  --dry-run=client -o yaml | kubectl apply -f -

if [ -n "${RULE_DATA_FILE}" ]; then
  kubectl create configmap "${RULE_CONFIGMAP_NAME}" \
    --namespace "${NAMESPACE}" \
    --from-file=rule.yaml="${RULE_CONFIG}" \
    --from-file="${RULE_DATA_KEY}=${RULE_DATA_FILE}" \
    --dry-run=client -o yaml | kubectl apply -f -
else
  kubectl create configmap "${RULE_CONFIGMAP_NAME}" \
    --namespace "${NAMESPACE}" \
    --from-file=rule.yaml="${RULE_CONFIG}" \
    --dry-run=client -o yaml | kubectl apply -f -
fi

if [ -n "${EXCLUSIONS_CONFIG}" ]; then
  kubectl create configmap "${EXCLUSIONS_CONFIGMAP_NAME}" \
    --namespace "${NAMESPACE}" \
    --from-file=exclusions.yaml="${EXCLUSIONS_CONFIG}" \
    --dry-run=client -o yaml | kubectl apply -f -
fi
