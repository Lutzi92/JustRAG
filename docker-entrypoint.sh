#!/bin/sh
set -e

# Fix permissions for data directories (in case of bind mounts)
# This requires the container to run as root initially
if [ "$(stat -c '%U' /app/data)" != "node" ]; then
    echo "Fixing permissions for /app/data..."
    chown -R node:node /app/data
fi

if [ "$(stat -c '%U' /app/uploads)" != "node" ]; then
    echo "Fixing permissions for /app/uploads..."
    chown -R node:node /app/uploads
fi

# Run migrations if enabled
if [ "$RUN_MIGRATIONS" = "true" ]; then
    echo "Running database migrations..."
    gosu node npm run db:migrate:prod

    echo "Running vector database migrations..."
    gosu node npm run migrate:vector:prod
fi

# Run seeding if enabled
if [ "$RUN_SEED" = "true" ]; then
    echo "Running database seeding..."
    gosu node npm run db:seed:prod
fi

# Start the application with the provided command
echo "Starting application with command: $@"
exec gosu node "$@"
