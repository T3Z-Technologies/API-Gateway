import httpx

from core.config import settings


async def sign_in_with_password(
    email: str,
    password: str,
) -> dict:

    url = (
        "https://identitytoolkit.googleapis.com/v1/"
        "accounts:signInWithPassword"
        f"?key={settings.firebase_api_key}"
    )

    async with httpx.AsyncClient(
        timeout=10.0
    ) as client:

        response = await client.post(
            url,
            json={
                "email": email,
                "password": password,
                "returnSecureToken": True,
            },
        )

    if response.status_code != 200:
        raise ValueError(
            "Invalid Firebase credentials"
        )

    return response.json()