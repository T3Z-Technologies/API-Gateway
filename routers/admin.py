import sqlite3
from datetime import datetime, timezone
from secrets import token_hex

from fastapi import (
    APIRouter,
    Depends,
    HTTPException,
)

from services.client_service import (
    provision_workflow,
)
from core.security import require_admin
from db.database import get_db
from models.schemas import (
    CreateClientRequest,
    CreateClientResponse,
    CreateWorkflowRequest,
    WorkflowResponse,
)
from services.client_service import (
    create_client,
    rotate_client_secret,
)
from services.n8n_service import (
    N8NError,
    n8n
)


router = APIRouter(
    prefix="/admin",
    tags=["Admin"],
)


@router.post(
    "/clients",
    response_model=CreateClientResponse,
)
async def admin_create_client(
    payload: CreateClientRequest,
    _: dict = Depends(require_admin),
):

    try:
        secret = await create_client(
            payload.client_id,
            payload.client_name,
        )

    except sqlite3.IntegrityError as exc:
        raise HTTPException(
            status_code=409,
            detail="Client already exists",
        ) from exc

    return {
        "client_id": payload.client_id,
        "client_name": payload.client_name,
        "client_secret": secret,
    }


@router.post(
    "/clients/{client_id}/rotate-secret",
    response_model=CreateClientResponse,
)
async def admin_rotate_secret(
    client_id: str,
    _: dict = Depends(require_admin),
):

    conn = get_db()

    client = conn.execute(
        """
        SELECT client_id, client_name
        FROM clients
        WHERE client_id = ?
        """,
        (client_id,),
    ).fetchone()

    conn.close()

    if not client:
        raise HTTPException(
            status_code=404,
            detail="Client not found",
        )

    secret = rotate_client_secret(
        client_id
    )

    return {
        "client_id": client["client_id"],
        "client_name": client["client_name"],
        "client_secret": secret,
    }


@router.post(
    "/clients/{client_id}/workflows",
    response_model=WorkflowResponse,
)
async def admin_create_workflow(
    client_id: str,
    payload: CreateWorkflowRequest,
    _: dict = Depends(require_admin),
):

    conn = get_db()

    client = conn.execute(
        """
        SELECT
            client_id,
            client_name,
            n8n_project_id,
            status
        FROM clients
        WHERE client_id = ?
        """,
        (client_id,),
    ).fetchone()

    conn.close()

    if not client:
        raise HTTPException(
            status_code=404,
            detail="Client not found",
        )

    if client["status"] != "active":
        raise HTTPException(
            status_code=400,
            detail="Client is inactive",
        )


    try:

        result = await provision_workflow(
            client_id=client_id,
            name=payload.name,
        )

    except Exception as exc:

        raise HTTPException(
            status_code=502,
            detail=(
                "Failed to provision "
                "n8n workflow"
            ),
        ) from exc

    return result