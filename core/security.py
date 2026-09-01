import base64
import hashlib
import hmac
import uuid
from datetime import datetime, timedelta, timezone
from pathlib import Path

import firebase_admin
import jwt
from argon2 import PasswordHasher
from argon2.exceptions import VerifyMismatchError
from fastapi import Depends, HTTPException, Request
from fastapi.security import (
    HTTPAuthorizationCredentials,
    HTTPBearer,
)

from core.config import settings


password_hasher = PasswordHasher()

bearer_scheme = HTTPBearer(
    auto_error=False
)


# ------------------------------------------------------------
# Firebase
# ------------------------------------------------------------

if not firebase_admin._apps:
    from firebase_admin import credentials

    firebase_admin.initialize_app(
        credentials.Certificate(
            settings.firebase_credentials
        )
    )


# ------------------------------------------------------------
# Client secret helpers
# ------------------------------------------------------------

def hash_secret(secret: str) -> str:
    return password_hasher.hash(secret)


def verify_secret(
    secret_hash: str,
    secret: str,
) -> bool:
    try:
        return password_hasher.verify(
            secret_hash,
            secret,
        )
    except VerifyMismatchError:
        return False


# ------------------------------------------------------------
# Client credential parsing
# ------------------------------------------------------------

def get_basic_credentials(
    request: Request,
) -> tuple[str, str]:

    authorization = request.headers.get(
        "Authorization",
        "",
    )

    if not authorization.startswith("Basic "):
        raise HTTPException(
            status_code=401,
            detail="Unauthorized",
            headers={
                "WWW-Authenticate": "Basic"
            },
        )

    try:
        raw = base64.b64decode(
            authorization[6:].strip(),
            validate=True,
        ).decode("utf-8")

        client_id, client_secret = raw.split(
            ":",
            1,
        )

    except (ValueError, UnicodeDecodeError):
        raise HTTPException(
            status_code=401,
            detail="Unauthorized",
            headers={
                "WWW-Authenticate": "Basic"
            },
        )

    return client_id, client_secret


# ------------------------------------------------------------
# JWT
# ------------------------------------------------------------

def load_private_key() -> str:
    return Path(
        settings.private_key_path
    ).read_text(
        encoding="utf-8"
    )


def load_public_key() -> str:
    return Path(
        settings.public_key_path
    ).read_text(
        encoding="utf-8"
    )


def create_workflow_token(
    client_id: str,
    workflow_id: str,
) -> str:

    now = datetime.now(
        timezone.utc
    )

    expiry = now + timedelta(
        seconds=settings.jwt_ttl_seconds
    )

    payload = {
        "iss": settings.jwt_issuer,
        "sub": client_id,
        "aud": settings.jwt_audience,
        "workflow": workflow_id,
        "iat": now,
        "exp": expiry,
        "jti": str(uuid.uuid4()),
    }

    return jwt.encode(
        payload,
        load_private_key(),
        algorithm=settings.jwt_algorithm,
    )


def verify_workflow_token(
    token: str,
    requested_workflow_id: str,
) -> dict:

    try:
        payload = jwt.decode(
            token,
            load_public_key(),
            algorithms=[
                settings.jwt_algorithm
            ],
            audience=settings.jwt_audience,
            issuer=settings.jwt_issuer,
        )

    except jwt.ExpiredSignatureError:
        raise HTTPException(
            status_code=401,
            detail="Token expired",
        )

    except jwt.PyJWTError:
        raise HTTPException(
            status_code=401,
            detail="Invalid token",
        )

    client_id = payload.get("sub")
    workflow_id = payload.get(
        "workflow"
    )

    if not client_id or not workflow_id:
        raise HTTPException(
            status_code=401,
            detail="Invalid token",
        )

    if not hmac.compare_digest(
        workflow_id,
        requested_workflow_id,
    ):
        raise HTTPException(
            status_code=403,
            detail=(
                "Token is not authorized "
                "for this workflow"
            ),
        )

    return payload


# ------------------------------------------------------------
# Firebase admin authorization
# ------------------------------------------------------------

async def get_current_firebase_user(
    credentials: (
        HTTPAuthorizationCredentials | None
    ) = Depends(bearer_scheme),
) -> dict:

    if credentials is None:
        raise HTTPException(
            status_code=401,
            detail="Authentication required",
        )

    from firebase_admin import (
        auth as firebase_auth,
    )

    try:
        return firebase_auth.verify_id_token(
            credentials.credentials
        )

    except Exception as exc:
        raise HTTPException(
            status_code=401,
            detail="Invalid Firebase ID token",
        ) from exc


async def require_admin(
    user: dict = Depends(
        get_current_firebase_user
    ),
) -> dict:

    if user.get("admin") is not True:
        raise HTTPException(
            status_code=403,
            detail="Admin access required",
        )

    return user