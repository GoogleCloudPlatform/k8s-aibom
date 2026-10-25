# Adopters and distributors

Who runs k8s-aibom, and who ships it. Add yourself with a pull request: one
row is enough, in your own words, and an organization name is all we ask.
Entries are facts the listed party has agreed to, not endorsements.

## Distributions that ship k8s-aibom

| Distribution | Since | How it ships | Reference |
|---|---|---|---|
| NVIDIA AI Cluster Recipes (AICR) | 2026-08 (v1.3.0); 1.6.x since 2026-10 | Qualified component under AICR's ADR-019: pinned chart, image and CRDs, requalified per release. Installed by default on the stock `h100-gke-cos-inference` and `h100-gke-cos-inference-dynamo` GKE recipes; one flag (`--runtime-inventory enabled`) on every other GKE recipe. | [component catalog](https://github.com/NVIDIA/aicr/blob/main/docs/user/component-catalog.md), [ADR-019](https://github.com/NVIDIA/aicr/blob/main/docs/design/019-k8s-aibom-runtime-inventory.md) |
| KubeAI | 2026-10 (v0.24.0) | Optional subchart, default off (`k8s-aibom.enabled`). | [how-to](https://github.com/kubeai-project/kubeai/blob/main/docs/how-to/inventory-ai-workloads-with-k8s-aibom.md) |
| vLLM production-stack | 2026-09 (chart 0.1.13) | Optional chart dependency, default off. | [production-stack#1099](https://github.com/vllm-project/production-stack/pull/1099) |

Distribution entries describe where the controller is installed from. They
do not say that any particular cluster has opted a namespace in; inventory
starts when an operator labels a namespace, by design.

## Adopters

Organizations running k8s-aibom on their own clusters and willing to say so.

| Organization | Since | How it is used |
|---|---|---|
| _your organization_ | | _one sentence, in your words_ |

## How to add an entry

Open a pull request editing this file. For an adopter row we need the
organization name, the month you started, and one sentence; a contact is
optional and never published without asking. For a distribution row, a link
to the shipping artifact (chart, recipe, registry entry) is enough.
