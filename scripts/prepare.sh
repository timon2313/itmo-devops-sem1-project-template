#!/bin/bash
set -e

echo "Installing Go dependencies..."
go mod tidy

if [ ! -f .env ]; then
  echo ".env file not found!"
  exit 1
fi

set -a
source .env
set +a

export PGPASSWORD="$POSTGRES_PASSWORD"

echo "Preparing database..."
psql -h "$POSTGRES_HOST" -p "$POSTGRES_PORT" -U "$POSTGRES_USER" -d "$POSTGRES_DB" -c "CREATE TABLE IF NOT EXISTS prices (id SERIAL PRIMARY KEY, created_at timestamp NOT NULL, name VARCHAR(255) NOT NULL, category VARCHAR(255) NOT NULL, price DECIMAL(10,2) NOT NULL);"

echo "Database prepared."
