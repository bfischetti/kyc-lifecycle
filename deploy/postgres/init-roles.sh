#!/usr/bin/env bash
# Provisions the cluster-level roles this service needs. Runs once, on first
# start of an empty Postgres data directory (docker-entrypoint-initdb.d).
#
# Roles are infrastructure, not schema, so they live here rather than in
# migrations. Migrations then grant table privileges to kyc_app.
#
#   kyc_owner  owns the database and every table; migrations run as it.
#   kyc_app    the server's runtime login. Not an owner, not a superuser,
#              cannot bypass RLS (ADR-0001: owners silently bypass RLS).
set -euo pipefail

: "${KYC_OWNER_PASSWORD:?KYC_OWNER_PASSWORD must be set}"
: "${KYC_APP_PASSWORD:?KYC_APP_PASSWORD must be set}"
: "${KYC_DB_NAME:=kyc}"

psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname postgres \
  -v owner_pw="$KYC_OWNER_PASSWORD" -v app_pw="$KYC_APP_PASSWORD" -v db="$KYC_DB_NAME" <<'SQL'
CREATE ROLE kyc_owner LOGIN PASSWORD :'owner_pw' NOSUPERUSER NOCREATEDB NOCREATEROLE;
CREATE ROLE kyc_app   LOGIN PASSWORD :'app_pw'   NOSUPERUSER NOCREATEDB NOCREATEROLE NOBYPASSRLS;
CREATE DATABASE :"db" OWNER kyc_owner;
REVOKE ALL ON DATABASE :"db" FROM PUBLIC;
GRANT CONNECT ON DATABASE :"db" TO kyc_app;
SQL
