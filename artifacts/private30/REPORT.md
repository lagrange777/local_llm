# Private LLM service — task 30

Executed on loopback Docker Desktop / ARM64 CPU with the real local Qwen model.
The user explicitly requested the Docker package and local checks for now. VPS deployment, Tailscale enrollment/HTTPS, and access from a second device are deferred.
The loopback run validates the application and Docker limits; it is not evidence of a deployed remote private service.

| Run | Complete | Passed | Checks |
|---|---|---|---|
| timeout | True | True | 1/1 |

**timeout / generation-timeout**: PASS
HTTP 200, 1.017 s. Answer: timeout
| smoke | True | True | 1/1 |

**smoke / restart-generation**: PASS
HTTP 200, 1.080 s. Answer: 4
| all | True | True | 35/35 |

**all / sequential-1**: PASS
HTTP 200, 13.644 s. Answer: 4

**all / sequential-2**: PASS
HTTP 200, 0.292 s. Answer: 4

**all / sequential-3**: PASS
HTTP 200, 0.270 s. Answer: 4

**all / sequential-4**: PASS
HTTP 200, 0.430 s. Answer: 4

**all / sequential-5**: PASS
HTTP 200, 0.517 s. Answer: 4

**all / sequential-6**: PASS
HTTP 200, 0.899 s. Answer: 4

**all / sequential-7**: PASS
HTTP 200, 2.011 s. Answer: 4

**all / sequential-8**: PASS
HTTP 200, 0.364 s. Answer: 4

**all / sequential-9**: PASS
HTTP 200, 0.287 s. Answer: 4

**all / sequential-10**: PASS
HTTP 200, 0.546 s. Answer: 4

**all / sequential-11**: PASS
HTTP 200, 0.241 s. Answer: 4

**all / sequential-12**: PASS
HTTP 200, 0.350 s. Answer: 4

**all / sequential-13**: PASS
HTTP 200, 2.029 s. Answer: 4

**all / sequential-14**: PASS
HTTP 200, 0.363 s. Answer: 4

**all / sequential-15**: PASS
HTTP 200, 0.199 s. Answer: 4

**all / sequential-16**: PASS
HTTP 200, 0.360 s. Answer: 4

**all / sequential-17**: PASS
HTTP 200, 0.252 s. Answer: 4

**all / sequential-18**: PASS
HTTP 200, 0.572 s. Answer: 4

**all / sequential-19**: PASS
HTTP 200, 1.748 s. Answer: 4

**all / sequential-20**: PASS
HTTP 200, 0.295 s. Answer: 4

**all / unauthorized-/api/status**: PASS
HTTP 401, 0.002 s. Answer: unauthorized

**all / unauthorized-/api/service/status**: PASS
HTTP 401, 0.002 s. Answer: unauthorized

**all / unauthorized-/api/optimization/report**: PASS
HTTP 401, 0.002 s. Answer: unauthorized

**all / unauthorized-/api/rag/search?query=history**: PASS
HTTP 401, 0.002 s. Answer: unauthorized

**all / wrong-key**: PASS
HTTP 401, 0.002 s. Answer: unauthorized

**all / foreign-origin**: PASS
HTTP 403, 0.002 s. Answer: origin

**all / body-limit**: PASS
HTTP 413, 0.002 s. Answer: none

**all / context-limit**: PASS
HTTP 400, 0.002 s. Answer: context_budget

**all / arbitrary-model**: PASS
HTTP 400, 0.001 s. Answer: none

**all / arbitrary-options**: PASS
HTTP 400, 0.001 s. Answer: none

**all / all-profiles-capped**: PASS

**all / bounded-parallel**: PASS
HTTP statuses: [200, 200, 429, 429, 200]
Max active/queued: 1/2

**all / parallel-slots-released**: PASS

**all / rolling-rate-limit**: PASS
HTTP statuses: [200, 200, 200, 200, 200, 200, 429]

**all / cancel-releases-generation**: PASS

Sequential successful-response median: 0.364 s.

## Artifacts

- [Sequential, parallel, auth and limit checks](all.json)
- [Real generation timeout](timeout.json)
- [Generation after application restart](smoke.json)
- [Docker resources, isolation and sanitized logs](environment.json)
- [Login UI](login.jpg)
- [Private chat UI](chat.jpg)

Credentials and session cookies are excluded. Tests use explicit public math questions; production logs contain only fixed route labels, status, timing and token count.
The HTTP test override disables Secure cookies only for loopback. The production configuration requires HTTPS and Secure cookies.
