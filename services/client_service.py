import secrets
from datetime import datetime, timezone

from core.security import hash_secret
from db.database import get_db

from services.n8n_service import (
    N8NError,
    n8n,
)


def generate_workflow_id() -> str:
    return (
        "wf_"
        + secrets.token_urlsafe(12)
        .replace("-", "")
        .replace("_", "")
    )


def generate_client_secret() -> str:
    return (
        "t3z_live_"
        + secrets.token_urlsafe(32)
    )


async def create_client(
    client_id: str,
    client_name: str,
) -> str:

    client_secret = generate_client_secret()

    secret_hash = hash_secret(
        client_secret
    )

    now = datetime.now(
        timezone.utc
    ).isoformat()

    # --------------------------------------------------
    # 1. Create the client's n8n folder
    # --------------------------------------------------

    try:

        folder = await n8n.create_folder(
            name=client_name,
        )

        n8n_folder_id = str(
            folder["id"]
        )

    except N8NError:
        raise

    # --------------------------------------------------
    # 2. Save client + folder + secret
    # --------------------------------------------------

    conn = get_db()

    try:

        conn.execute(
            """
            INSERT INTO clients (
                client_id,
                client_name,
                status,
                n8n_folder_id
            )
            VALUES (?, ?, 'active', ?)
            """,
            (
                client_id,
                client_name,
                n8n_folder_id,
            ),
        )

        conn.execute(
            """
            INSERT INTO client_credentials (
                client_id,
                secret_hash,
                status,
                created_at
            )
            VALUES (?, ?, ?, ?)
            """,
            (
                client_id,
                secret_hash,
                "active",
                now,
            ),
        )

        conn.commit()

    except Exception:

        conn.rollback()

        # DB failed after n8n folder creation.
        # Remove the orphaned folder.

        await n8n.delete_folder(
            n8n_folder_id
        )

        raise

    finally:
        conn.close()

    return client_secret


async def provision_workflow(
    *,
    client_id: str,
    name: str,
) -> dict:

    workflow_id = generate_workflow_id()

    webhook_path = (
        f"{client_id}/{workflow_id}"
    )

    # --------------------------------------------------
    # 1. Get client's n8n folder
    # --------------------------------------------------

    conn = get_db()

    client = conn.execute(
        """
        SELECT
            client_id,
            status,
            n8n_folder_id
        FROM clients
        WHERE client_id = ?
        """,
        (client_id,),
    ).fetchone()

    conn.close()

    if not client:
        raise ValueError(
            "Client not found"
        )

    if client["status"] != "active":
        raise ValueError(
            "Client is inactive"
        )

    client_folder_id = client[
        "n8n_folder_id"
    ]

    if not client_folder_id:
        raise ValueError(
            "Client does not have an n8n folder"
        )

    # --------------------------------------------------
    # 2. Create workflow in client's n8n folder
    # --------------------------------------------------

    try:

        n8n_workflow = (
            await n8n.create_workflow(
                name=name,
                webhook_path=webhook_path,
                parent_folder_id=(
                    client_folder_id
                ),
            )
        )

        n8n_workflow_id = str(
            n8n_workflow["id"]
        )

    except N8NError:
        raise

    # --------------------------------------------------
    # 3. Activate n8n workflow
    # --------------------------------------------------

    try:

        await n8n.activate_workflow(
            n8n_workflow_id
        )

    except N8NError:

        # Workflow exists but couldn't
        # be activated. Clean it up.

        await n8n.delete_workflow(
            n8n_workflow_id
        )

        raise

    # --------------------------------------------------
    # 4. Register T3Z relationship
    # --------------------------------------------------

    created_at = datetime.now(
        timezone.utc
    ).isoformat()

    conn = get_db()

    try:

        conn.execute(
            """
            INSERT INTO client_workflows (
                client_id,
                workflow_id,
                n8n_workflow_id,
                workflow_name,
                webhook_path,
                status,
                created_at
            )
            VALUES (
                ?,
                ?,
                ?,
                ?,
                ?,
                'active',
                ?
            )
            """,
            (
                client_id,
                workflow_id,
                n8n_workflow_id,
                name,
                webhook_path,
                created_at,
            ),
        )

        conn.commit()

    except Exception:

        conn.rollback()

        # DB registration failed,
        # so remove the n8n workflow too.

        await n8n.delete_workflow(
            n8n_workflow_id
        )

        raise

    finally:
        conn.close()

    # --------------------------------------------------
    # 5. Return provisioning result
    # --------------------------------------------------

    return {
        "client_id": client_id,
        "workflow_id": workflow_id,
        "n8n_workflow_id": n8n_workflow_id,
        "name": name,
        "webhook_path": webhook_path,
        "webhook_url": (
            "https://api.t3z.in/"
            "webhooks/"
            f"{workflow_id}"
        ),
        "status": "active",
    }


def rotate_client_secret(
    client_id: str,
) -> str:

    new_secret = generate_client_secret()

    secret_hash = hash_secret(
        new_secret
    )

    now = datetime.now(
        timezone.utc
    ).isoformat()

    conn = get_db()

    try:

        conn.execute(
            """
            UPDATE client_credentials
            SET status = 'revoked',
                revoked_at = ?
            WHERE client_id = ?
              AND status = 'active'
            """,
            (
                now,
                client_id,
            ),
        )

        conn.execute(
            """
            INSERT INTO client_credentials (
                client_id,
                secret_hash,
                status,
                created_at
            )
            VALUES (?, ?, ?, ?)
            """,
            (
                client_id,
                secret_hash,
                "active",
                now,
            ),
        )

        conn.commit()

    except Exception:

        conn.rollback()
        raise

    finally:
        conn.close()

    return new_secret