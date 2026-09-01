## Note 1

Set `admin: True` custom class in Firebase through the script 

```python
python - <<'PY'
import os
import firebase_admin
from firebase_admin import credentials, auth

if not firebase_admin._apps:
    firebase_admin.initialize_app(
        credentials.Certificate(
            os.environ["GOOGLE_APPLICATION_CREDENTIALS"]
        )
    )

uid = "55HRzVC7PMTTIXEKhHCdUAL56sp1"

auth.set_custom_user_claims(
    uid,
    {"admin": True}
)

print("Admin claim set.")
PY
```