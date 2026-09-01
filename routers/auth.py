import sqlite3

from fastapi import (
    APIRouter,
    HTTPException,
    Request,
)

from core.config import settings
from core.security import (
    get_basic_credentials,
    verify_secret,
)
from db.database import get_db
from models.schemas import (
    FirebaseLoginRequest,
    FirebaseLoginResponse,
    TokenRequest,
    TokenResponse,
)
from services.firebase_service import (
    sign_in_with_password,
)
from services.token_service import (
    issue_workflow_token,
)


router = APIRouter(
    prefix="/auth",
    tags=["Authentication"],
)


@router.post(
    "/firebase-login",
    response_model=FirebaseLoginResponse,
)
async def firebase_login(
    payload: FirebaseLoginRequest,
):

    try:
        data = await sign_in_with_password(
            payload.email,
            payload.password,
        )

    except ValueError:
        raise HTTPException(
            status_code=401,
            detail="Invalid Firebase credentials",
        )

    # Only return the ID token.
    # Do NOT expose Firebase refresh tokens.
    return {
        "access_token": data["idToken"],
        "token_type": "bearer",
        "expires_in": int(
            data["expiresIn"]
        ),
    }


@router.post(
    "/token",
    response_model=TokenResponse,
)
async def issue_token(
    payload: TokenRequest,
    request: Request,
):

    client_id, client_secret = (
        get_basic_credentials(request)
    )

    conn = get_db()

    try:
        client = conn.execute(
            """
            SELECT client_id, status
            FROM clients
            WHERE client_id = ?
            """,
            (client_id,),
        ).fetchone()

        credentials = conn.execute(
            """
            SELECT secret_hash
            FROM client_credentials
            WHERE client_id = ?
              AND status = 'active'
            ORDER BY id DESC
            """,
            (client_id,),
        ).fetchall()

        workflow = conn.execute(
            """
            SELECT workflow_id
            FROM client_workflows
            WHERE client_id = ?
              AND workflow_id = ?
              AND status = 'active'
            """,
            (
                client_id,
                payload.workflow_id,
            ),
        ).fetchone()

    finally:
        conn.close()

    if (
        not client
        or client["status"] != "active"
    ):
        raise HTTPException(
            status_code=401,
            detail="Unauthorized",
        )

    if not any(
        verify_secret(
            row["secret_hash"],
            client_secret,
        )
        for row in credentials
    ):
        raise HTTPException(
            status_code=401,
            detail="Unauthorized",
        )

    if not workflow:
        raise HTTPException(
            status_code=403,
            detail="Forbidden",
        )

    token = issue_workflow_token(
        client_id,
        payload.workflow_id,
    )

    return {
        "access_token": token,
        "token_type": "Bearer",
        "expires_in": (
            settings.jwt_ttl_seconds
        ),
        "workflow_id": (
            payload.workflow_id
        ),
    }