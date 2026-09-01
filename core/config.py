import os
from pathlib import Path


class Settings:
    base_dir = Path(__file__).resolve().parent.parent

    db_path = Path(
        os.getenv(
            "T3Z_DB_PATH",
            str(base_dir / "data" / "auth.db"),
        )
    )

    n8n_project_id = os.getenv(
        "N8N_PROJECT_ID",
        "FxmDzi016TxPaOQb",
    )

    private_key_path = Path(
        os.getenv(
            "T3Z_JWT_PRIVATE_KEY",
            str(base_dir / "keys" / "private.pem"),
        )
    )

    public_key_path = Path(
        os.getenv(
            "T3Z_JWT_PUBLIC_KEY",
            str(base_dir / "keys" / "public.pem"),
        )
    )

    jwt_issuer = os.getenv(
        "T3Z_JWT_ISSUER",
        "t3z.in",
    )

    jwt_audience = os.getenv(
        "T3Z_JWT_AUDIENCE",
        "t3z-api",
    )

    jwt_algorithm = "RS256"

    jwt_ttl_seconds = int(
        os.getenv(
            "T3Z_JWT_TTL_SECONDS",
            "3600",
        )
    )

    firebase_api_key = os.environ[
        "T3Z_FIREBASE_API_KEY"
    ]

    firebase_credentials = os.environ[
        "GOOGLE_APPLICATION_CREDENTIALS"
    ]

    n8n_base_url = os.getenv(
        "N8N_BASE_URL",
        "http://127.0.0.1:5678",
    ).rstrip("/")

    n8n_api_key = os.environ[
        "N8N_API_KEY"
    ]

    n8n_webhook_prefix = os.getenv(
        "N8N_WEBHOOK_PREFIX",
        "webhooks",
    ).strip("/")


settings = Settings()