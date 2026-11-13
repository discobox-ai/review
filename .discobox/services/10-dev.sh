#!/bin/bash
#---
# name: Dev build
# description: Rebuilds build/discobox-review whenever its source changes
#---

set -euo pipefail

exec go tool task dev
