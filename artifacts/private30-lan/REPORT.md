# Home LAN HTTP check

Executed from the server Mac using its LAN IPv4. Access from a second physical device is not yet verified.

- 11 authorization/input/profile limit checks passed.
- Real model API answer passed.
- LAN and loopback browser origins accepted; foreign origin rejected.
- LAN web chat answered 7 x 8 with 56 in 2.8 s.
- Gateway published on loopback and the selected home IP only. Ollama remains unpublished.
- HTTP was explicitly requested by the owner; encryption is disabled for this opt-in mode.

[Limit checks](limits.json), [Model API](smoke.json), [Environment](environment.json), [Chat](chat.jpg).
