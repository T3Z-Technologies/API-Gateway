import sqlite3

from core.config import settings


def get_db() -> sqlite3.Connection:

    settings.db_path.parent.mkdir(
        parents=True,
        exist_ok=True,
    )

    conn = sqlite3.connect(
        settings.db_path,
        timeout=10,
    )

    conn.row_factory = sqlite3.Row

    conn.execute(
        "PRAGMA foreign_keys = ON"
    )

    return conn


def ensure_column(
    conn: sqlite3.Connection,
    table: str,
    column: str,
    definition: str,
) -> None:

    columns = {
        row["name"]
        for row in conn.execute(
            f"PRAGMA table_info({table})"
        ).fetchall()
    }

    if column not in columns:
        conn.execute(
            f"""
            ALTER TABLE {table}
            ADD COLUMN {column} {definition}
            """
        )


def init_db() -> None:

    conn = get_db()

    conn.execute("""
        CREATE TABLE IF NOT EXISTS clients (
            client_id TEXT PRIMARY KEY,
            client_name TEXT NOT NULL,
            status TEXT NOT NULL DEFAULT 'active',
            n8n_folder_id TEXT
        )
    """)

    conn.execute("""
        CREATE TABLE IF NOT EXISTS client_credentials (
            id INTEGER PRIMARY KEY AUTOINCREMENT,
            client_id TEXT NOT NULL,
            secret_hash TEXT NOT NULL,
            status TEXT NOT NULL DEFAULT 'active',
            created_at TEXT NOT NULL,
            revoked_at TEXT,
            FOREIGN KEY (client_id)
                REFERENCES clients(client_id)
                ON DELETE CASCADE
        )
    """)

    conn.execute("""
        CREATE TABLE IF NOT EXISTS client_workflows (
            client_id TEXT NOT NULL,
            workflow_id TEXT NOT NULL,
            n8n_workflow_id TEXT,
            workflow_name TEXT,
            webhook_path TEXT,
            status TEXT NOT NULL DEFAULT 'active',
            created_at TEXT NOT NULL,
            PRIMARY KEY (
                client_id,
                workflow_id
            ),
            FOREIGN KEY (client_id)
                REFERENCES clients(client_id)
                ON DELETE CASCADE
        )
    """)

    # Migrate your existing tables.

    ensure_column(
        conn,
        "clients",
        "n8n_project_id",
        "TEXT",
    )

    ensure_column(
        conn,
        "client_workflows",
        "n8n_workflow_id",
        "TEXT",
    )

    ensure_column(
        conn,
        "client_workflows",
        "workflow_name",
        "TEXT",
    )

    ensure_column(
        conn,
        "client_workflows",
        "webhook_path",
        "TEXT",
    )

    ensure_column(
        conn,
        "client_workflows",
        "created_at",
        "TEXT NOT NULL DEFAULT ''",
    )

    conn.commit()
    conn.close()