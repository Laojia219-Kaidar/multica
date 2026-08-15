#!/bin/bash
# hivecrew-db-backup — HiveCrew 生产数据库 + 工件上传目录 每日备份到 NAS。
# DB: pg_dump 自定义格式；工件: docker volume multica_backend_uploads 打 tar.gz。
# 目标目录在 HiveData（NAS 卷）；各保留最近 14 份，超出轮转删除。
# 由 launchd com.hivecosm.hivecrew-db-backup 每日触发。
#
# 写入路径说明：launchd 进程对可移除卷无 TCC 授权（交互终端才有），
# 直接重定向会 EPERM。因此产物先落到本地 /tmp，再由挂载了目标目录的
# alpine 容器执行最终落盘与轮转（Docker Desktop 的卷共享授权独立于
# launchd）。
set -euo pipefail

# launchd 默认 PATH 只有系统目录；docker 在 /usr/local/bin。
DOCKER="/usr/local/bin/docker"
[ -x "${DOCKER}" ] || DOCKER="$(command -v docker || true)"
if [ -z "${DOCKER}" ]; then
  echo "ERROR: docker not found" >&2
  exit 1
fi

BACKUP_DIR="/Volumes/HiveData/backups/db-backups"
ARTIFACT_DIR="/Volumes/HiveData/backups/artifact-uploads"
KEEP=14
STAMP="$(date +%Y%m%d-%H%M%S)"
PG=multica-postgres-1
UPLOADS_VOLUME="multica_backend_uploads"

# NAS 未挂载时绝不能把备份悄悄写进挂载点下的本地盘
if ! mount | grep -q " on /Volumes/HiveData "; then
  echo "ERROR: /Volumes/HiveData not mounted; aborting" >&2
  exit 1
fi
mkdir -p "${BACKUP_DIR}" "${ARTIFACT_DIR}"

# --- 1. 数据库 ---
DB_OUT="multica-${STAMP}.dump"
"${DOCKER}" exec "${PG}" pg_dump -U multica -F c -f "/tmp/${DB_OUT}" multica
"${DOCKER}" cp "${PG}:/tmp/${DB_OUT}" "/tmp/hivecrew-db-backup-${STAMP}"
"${DOCKER}" exec "${PG}" rm -f "/tmp/${DB_OUT}"

# --- 2. 工件上传目录（docker volume → tar.gz） ---
ART_OUT="uploads-${STAMP}.tar.gz"
"${DOCKER}" run --rm -v "${UPLOADS_VOLUME}:/src:ro" -v /tmp:/out alpine \
  sh -c "cd /src && tar czf /out/hivecrew-uploads-backup-${STAMP}.tar.gz ."

# --- 3. 落盘 + 轮转都在持有 TCC 授权的容器内完成 ---
"${DOCKER}" run --rm -v "${BACKUP_DIR}:/dbbackup" -v "${ARTIFACT_DIR}:/artbackup" -v /tmp:/src alpine sh -c "
  mv /src/hivecrew-db-backup-${STAMP} /dbbackup/${DB_OUT}
  mv /src/hivecrew-uploads-backup-${STAMP}.tar.gz /artbackup/${ART_OUT}
  cd /dbbackup && ls -1 multica-*.dump | sort -r | tail -n +$((KEEP + 1)) | xargs -r rm -f
  cd /artbackup && ls -1 uploads-*.tar.gz | sort -r | tail -n +$((KEEP + 1)) | xargs -r rm -f
"

echo "backup ok: ${BACKUP_DIR}/${DB_OUT} + ${ARTIFACT_DIR}/${ART_OUT}"
