#!/bin/bash
# Executed by the postgres container at startup.
# Creates the databases that the directory-peer and each org need.

set -euo pipefail

psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "postgres" <<-EOSQL
    CREATE DATABASE fsc_dir_controller;
    CREATE DATABASE fsc_dir_manager;
    CREATE DATABASE fsc_dir_txlog;
    CREATE DATABASE fsc_edi_controller;
    CREATE DATABASE fsc_edi_manager;
    CREATE DATABASE fsc_edi_txlog;
    CREATE DATABASE fsc_bd_controller;
    CREATE DATABASE fsc_bd_manager;
    CREATE DATABASE fsc_bd_txlog;
    CREATE DATABASE fsc_hv_controller;
    CREATE DATABASE fsc_hv_manager;
    CREATE DATABASE fsc_hv_txlog;
EOSQL
