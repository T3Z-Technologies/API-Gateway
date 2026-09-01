import os
import sys

import firebase_admin
from firebase_admin import auth, credentials


if len(sys.argv) != 2:
    print(
        "Usage: "
        "python scripts/set_firebase_admin.py "
        "<firebase_uid>"
    )
    raise SystemExit(1)


uid = sys.argv[1]

credential_path = os.environ[
    "GOOGLE_APPLICATION_CREDENTIALS"
]

if not firebase_admin._apps:
    firebase_admin.initialize_app(
        credentials.Certificate(
            credential_path
        )
    )

auth.set_custom_user_claims(
    uid,
    {
        "admin": True
    },
)

print(
    f"Admin claim set for {uid}"
)

print(
    "Sign out/in to obtain a fresh ID token."
)