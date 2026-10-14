# Separate web password and API key

- Browser login uses a separate password; API uses the original Bearer key.
- 6 live HTTP authentication-separation checks passed.
- Wrong password, API key used as password, legacy token login body and web password used as Bearer were rejected.
- Docker app receives only the salted PBKDF2-SHA256 hash and API key; the plaintext initial password is not mounted.
- Web login succeeded; model answered 9 x 9 with 81 in 3.1 s.
- Go race tests, vet, Docker build and syntax checks passed.

[Checks](checks.json), [API generation](smoke.json), [Password login screen](login.jpg). Credentials are excluded.

All app/gateway/Ollama containers were restarted. [Post-restart API generation](after-restart/smoke.json).
