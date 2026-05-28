#!/bin/bash
set -e

# =============================================================================
# PostgreSQL 16 → 18 Migration Script
#
# Migrates the main database (rag_db) from PostgreSQL 16 to 18.
# Uses per-database pg_dump (custom format) to avoid pg_dumpall
# compatibility issues between major versions.
#
# Usage: Run from the project root directory.
#   ./scripts/migrate_db_16_to_18.sh
#
# Prerequisites:
#   - Docker must be available
#   - All containers must be stopped (docker compose down)
#   - The old PG16 volume must exist
#
# What this script does:
#   1. Stops all running containers
#   2. Spins up a temporary PG16 container on the old volume
#   3. Dumps rag_db using pg_dump (custom format, version-independent)
#   4. Creates a new volume and spins up a temporary PG18 container
#   5. Creates the database and restores the dump
#   6. Cleans up temporary containers
#
# After running:
#   - docker-compose.yml should reference the new volume (rag-db-data-v18)
#   - Run: docker compose up -d
# =============================================================================

# Configuration
OLD_DB_IMAGE="postgres:16-alpine"
NEW_DB_IMAGE="postgres:18-alpine"
BASE_OLD_VOLUME_NAME="rag-db-data"
BASE_NEW_VOLUME_NAME="rag-db-data-v18"
BACKUP_FILE="rag_db_backup_16.dump"
TEMP_CONTAINER_OLD="pg16_migrate_temp"
TEMP_CONTAINER_NEW="pg18_migrate_temp"
MAX_RETRIES=60

# Read DB config from .env if available
DB_USER="${DB_USER:-postgres}"
DB_PASSWORD="${DB_PASSWORD:-postgres}"
DB_NAME="${DB_NAME:-rag_db}"

if [ -f .env ]; then
    # Read only DB_ vars from .env without evaluating file content.
    while IFS='=' read -r key value; do
        # Skip empty/comment lines quickly.
        [ -z "$key" ] && continue
        [[ "$key" =~ ^[[:space:]]*# ]] && continue

        # Trim surrounding whitespace in key/value.
        key="${key#"${key%%[![:space:]]*}"}"
        key="${key%"${key##*[![:space:]]}"}"
        value="${value#"${value%%[![:space:]]*}"}"
        value="${value%"${value##*[![:space:]]}"}"

        # Remove matching surrounding quotes, then trim whitespace again.
        if [[ "$value" == \"*\" && "$value" == *\" ]]; then
            value="${value:1:-1}"
        elif [[ "$value" == \'*\' && "$value" == *\' ]]; then
            value="${value:1:-1}"
        fi
        value="${value#"${value%%[![:space:]]*}"}"
        value="${value%"${value##*[![:space:]]}"}"

        case "$key" in
            DB_USER|DB_PASSWORD|DB_NAME)
                export "$key=$value"
                ;;
        esac
    done < .env
fi

# -------------------------------------------------------------------
# Helper functions
# -------------------------------------------------------------------

cleanup() {
    echo ""
    echo "Cleaning up temporary containers..."
    docker rm -f "$TEMP_CONTAINER_OLD" 2>/dev/null || true
    docker rm -f "$TEMP_CONTAINER_NEW" 2>/dev/null || true
}

trap cleanup EXIT

wait_for_pg() {
    local container="$1"
    local retries=0
    echo "Waiting for PostgreSQL to be ready in $container..."
    until docker exec "$container" pg_isready -U "$DB_USER" -q 2>/dev/null; do
        if [ $retries -ge $MAX_RETRIES ]; then
            echo "Error: Timed out waiting for PostgreSQL in $container after ${MAX_RETRIES}s."
            exit 1
        fi
        sleep 1
        retries=$((retries + 1))
    done
    echo "PostgreSQL is ready."
}

sanitize_integer() {
    local value="$1"
    value="${value//[!0-9]/}"
    if [ -z "$value" ]; then
        echo "0"
    else
        echo "$value"
    fi
}

# -------------------------------------------------------------------
# Pre-flight checks
# -------------------------------------------------------------------

if ! command -v docker &> /dev/null; then
    echo "Error: docker command not found."
    exit 1
fi

# Warn if containers are still running
RUNNING=$(docker compose ps -q 2>/dev/null | wc -l)
if [ "$RUNNING" -gt 0 ]; then
    echo "Warning: Docker Compose services are still running."
    read -p "Stop all services before migrating? (Y/n) " -n 1 -r
    echo
    if [[ ! $REPLY =~ ^[Nn]$ ]]; then
        echo "Stopping services..."
        docker compose down
    else
        echo "Error: Cannot migrate while services are running."
        exit 1
    fi
fi

echo "============================================="
echo "  PostgreSQL 16 → 18 Migration"
echo "============================================="
echo ""

# -------------------------------------------------------------------
# Step 1: Detect old volume
# -------------------------------------------------------------------

VOLUMES=$(docker volume ls -q | grep -E "(^|[_])${BASE_OLD_VOLUME_NAME}$" || true)
COUNT=$(echo "$VOLUMES" | wc -w)

if [ "$COUNT" -eq 0 ] || [ -z "$VOLUMES" ]; then
    echo "Error: No volume matching '*${BASE_OLD_VOLUME_NAME}' found."
    echo "Available volumes:"
    docker volume ls -q | grep -i "rag\|db\|postgres" || echo "  (none)"
    exit 1
elif [ "$COUNT" -eq 1 ]; then
    OLD_VOLUME_NAME=$(echo "$VOLUMES" | tr -d '[:space:]')
    echo "Found old volume: $OLD_VOLUME_NAME"
else
    echo "Multiple matching volumes found:"
    echo "$VOLUMES"
    read -p "Enter the full volume name to migrate: " -r OLD_VOLUME_NAME
    if ! docker volume inspect "$OLD_VOLUME_NAME" &> /dev/null; then
        echo "Error: Volume '$OLD_VOLUME_NAME' not found."
        exit 1
    fi
fi

# -------------------------------------------------------------------
# Step 2: Determine new volume name
# -------------------------------------------------------------------

if [[ "$OLD_VOLUME_NAME" == *"$BASE_OLD_VOLUME_NAME" ]]; then
    PREFIX=${OLD_VOLUME_NAME%$BASE_OLD_VOLUME_NAME}
    NEW_VOLUME_NAME="${PREFIX}${BASE_NEW_VOLUME_NAME}"
else
    NEW_VOLUME_NAME="$BASE_NEW_VOLUME_NAME"
fi

echo "Target new volume: $NEW_VOLUME_NAME"

if docker volume inspect "$NEW_VOLUME_NAME" &> /dev/null; then
    echo ""
    echo "Warning: Volume '$NEW_VOLUME_NAME' already exists."
    read -p "Remove it and start fresh? (y/N) " -n 1 -r
    echo
    if [[ ! $REPLY =~ ^[Yy]$ ]]; then
        echo "Aborting."
        exit 1
    fi
    docker volume rm "$NEW_VOLUME_NAME"
fi

echo ""

# -------------------------------------------------------------------
# Step 3: Dump from PG16
# -------------------------------------------------------------------

echo "Step 1/3: Dumping database from PostgreSQL 16..."
echo "  Volume: $OLD_VOLUME_NAME"
echo "  Database: $DB_NAME"
echo ""

# Remove stale temp container if present
docker rm -f "$TEMP_CONTAINER_OLD" 2>/dev/null || true

# Start temporary PG16 container
docker run -d --name "$TEMP_CONTAINER_OLD" \
    -v "$OLD_VOLUME_NAME":/var/lib/postgresql/data \
    -e POSTGRES_HOST_AUTH_METHOD=trust \
    "$OLD_DB_IMAGE" \
    postgres

wait_for_pg "$TEMP_CONTAINER_OLD"

# Use pg_dump with custom format (-Fc) for version-independent backup.
# This avoids pg_dumpall's \restrict/\unrestrict commands that break
# cross-version restores.
echo "Running pg_dump..."
docker exec "$TEMP_CONTAINER_OLD" \
    pg_dump -U "$DB_USER" -d "$DB_NAME" -Fc --no-owner --no-acl \
    -f "/tmp/$BACKUP_FILE"

# Copy dump out of container
docker cp "$TEMP_CONTAINER_OLD:/tmp/$BACKUP_FILE" "./$BACKUP_FILE"

# Stop and remove temp container
docker rm -f "$TEMP_CONTAINER_OLD"

DUMP_SIZE=$(du -h "./$BACKUP_FILE" | cut -f1)
echo "Backup complete: $BACKUP_FILE ($DUMP_SIZE)"
echo ""

# -------------------------------------------------------------------
# Step 4: Restore into PG18
# -------------------------------------------------------------------

echo "Step 2/3: Restoring database into PostgreSQL 18..."

# Create new volume
docker volume create "$NEW_VOLUME_NAME"

# Remove stale temp container if present
docker rm -f "$TEMP_CONTAINER_NEW" 2>/dev/null || true

# Start temporary PG18 container.
# PostgreSQL 18's Docker image changed PGDATA to /var/lib/postgresql/18/docker,
# so the volume must be mounted at /var/lib/postgresql (not .../data).
docker run -d --name "$TEMP_CONTAINER_NEW" \
    -v "$NEW_VOLUME_NAME":/var/lib/postgresql \
    -e POSTGRES_USER="$DB_USER" \
    -e POSTGRES_PASSWORD="$DB_PASSWORD" \
    -e POSTGRES_DB="$DB_NAME" \
    "$NEW_DB_IMAGE" \
    postgres

wait_for_pg "$TEMP_CONTAINER_NEW"

# Wait for database initialization to fully complete.
# The postgres Docker entrypoint restarts the server after init, so pg_isready
# can succeed against the temporary init server before the final restart.
echo "Waiting for database initialization to complete..."
retries=0
until docker exec "$TEMP_CONTAINER_NEW" \
    psql -U "$DB_USER" -d "$DB_NAME" -c "SELECT 1;" &>/dev/null; do
    if [ $retries -ge $MAX_RETRIES ]; then
        echo "Error: Timed out waiting for database '$DB_NAME' to become available."
        exit 1
    fi
    sleep 1
    retries=$((retries + 1))
done
echo "Database is ready."

# Enable pg_stat_statements extension (matches docker-compose config)
docker exec "$TEMP_CONTAINER_NEW" \
    psql -U "$DB_USER" -d "$DB_NAME" -c "CREATE EXTENSION IF NOT EXISTS pg_stat_statements;" 2>/dev/null || true

# Copy dump into container and restore
docker cp "./$BACKUP_FILE" "$TEMP_CONTAINER_NEW:/tmp/$BACKUP_FILE"

echo "Running pg_restore..."
# pg_restore with -Fc format. Use --clean to drop objects before recreating,
# --if-exists to avoid errors on first run, and --single-transaction for atomicity.
docker exec "$TEMP_CONTAINER_NEW" \
    pg_restore -U "$DB_USER" -d "$DB_NAME" \
    --no-owner --no-acl \
    --clean --if-exists \
    --single-transaction \
    "/tmp/$BACKUP_FILE" || {
        # pg_restore returns non-zero on warnings (e.g. "relation does not exist" during --clean).
        # Check if the database actually has data.
        ROW_COUNT=$(docker exec "$TEMP_CONTAINER_NEW" \
            psql -U "$DB_USER" -d "$DB_NAME" -tAc \
            "SELECT SUM(n_live_tup) FROM pg_stat_user_tables;")
        ROW_COUNT=$(sanitize_integer "$ROW_COUNT")
        if [ -z "$ROW_COUNT" ] || [ "$ROW_COUNT" -eq 0 ]; then
            echo "Error: Restore produced an empty database."
            exit 1
        fi
        echo "Note: pg_restore reported warnings (this is normal with --clean --if-exists)."
    }

echo "Restore complete."
echo ""

# -------------------------------------------------------------------
# Step 5: Verify (reuse the same running container to avoid restart issues)
# -------------------------------------------------------------------

echo "Step 3/3: Verifying migration..."

# Force a checkpoint so all data is flushed to the volume before we verify.
docker exec "$TEMP_CONTAINER_NEW" \
    psql -U "$DB_USER" -d "$DB_NAME" -c "CHECKPOINT;" >/dev/null

# Run ANALYZE so pg_stat_user_tables has up-to-date row counts.
docker exec "$TEMP_CONTAINER_NEW" \
    psql -U "$DB_USER" -d "$DB_NAME" -c "ANALYZE;" >/dev/null

echo ""
echo "Table row counts in migrated database:"
echo "---------------------------------------"
docker exec "$TEMP_CONTAINER_NEW" \
    psql -U "$DB_USER" -d "$DB_NAME" -c "
        SELECT relname AS table,
               n_live_tup AS rows
        FROM pg_stat_user_tables
        WHERE schemaname = 'public'
        ORDER BY relname;
    "

TOTAL=$(docker exec "$TEMP_CONTAINER_NEW" \
    psql -U "$DB_USER" -d "$DB_NAME" -tAc \
    "SELECT SUM(n_live_tup) FROM pg_stat_user_tables WHERE schemaname = 'public';")
TOTAL=$(sanitize_integer "$TOTAL")

# Stop container gracefully so PostgreSQL flushes all data to the volume.
docker stop "$TEMP_CONTAINER_NEW" >/dev/null
docker rm "$TEMP_CONTAINER_NEW" >/dev/null

echo ""
if [ "$TOTAL" -gt 0 ]; then
    echo "============================================="
    echo "  Migration successful! ($TOTAL total rows)"
    echo "============================================="
else
    echo "Warning: No data found in migrated database."
    echo "The old volume has NOT been removed - you can retry."
    exit 1
fi

echo ""
echo "Next steps:"
echo "  1. Ensure docker-compose.yml uses volume '$BASE_NEW_VOLUME_NAME'"
echo "     and image '$NEW_DB_IMAGE' for the db service."
echo "  2. Run: docker compose up -d"
echo "  3. Verify the application works correctly."
echo "  4. (Optional) Remove old volume: docker volume rm $OLD_VOLUME_NAME"
echo "  5. (Optional) Remove backup: rm $BACKUP_FILE"
