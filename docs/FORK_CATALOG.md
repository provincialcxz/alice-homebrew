# Каталог форка provincialcxz

Сборка форка показывает два встроенных источника: каталог автора (`official`) и этот каталог (`fork`, `docs/catalog/`). У каждого свой закреплённый ключ в `internal/homebrew/sources.go`; чужой ключ для этих адресов не принимается.

- Публичный ключ: `7go3MoIoHhA0NLJIOnpb83zdHxaTCWB6NjvGmTEmhfw=` (`ForkCatalogKey`, `docs/public-key.txt`).
- Данные модератора и **закрытый** ключ: `%LOCALAPPDATA%\AliceHomebrewModerator` на ПК владельца. Никогда не коммитьте эту папку. Сделайте резервную копию: без `catalog-signing.key` подписать обновление каталога будет нечем, и придётся выпускать менеджер с новым ключом.
- Плагины API 3 есть только в `v3.json`; `v1`/`v2` нужны старым менеджерам.
- Каталог действителен год. `TestPublishedForkCatalog` начнёт падать за 30 дней до срока: значит, пора переэкспортировать.

## Добавить или обновить плагин

```powershell
go run ./cmd/alice-homebrew pack examples/ИМЯ ИМЯ.aliceplugin
go run ./cmd/alice-homebrew store --data "$env:LOCALAPPDATA\AliceHomebrewModerator" --listen 127.0.0.1:8089
```

Откройте `http://127.0.0.1:8089`, войдите кодом из `admin-token.txt` в той же папке, отправьте пакет, проверьте и одобрите. Остановите магазин и экспортируйте:

```powershell
go run ./cmd/alice-homebrew export "$env:LOCALAPPDATA\AliceHomebrewModerator" export.zip https://github.com/provincialcxz/alice-homebrew
```

Из архива замените в репозитории только `docs/catalog/`, `docs/packages/`, `docs/index.html`, `docs/store.js`, `docs/style.css`, `docs/public-key.txt`, `docs/site.json` и `docs/.nojekyll`. Запустите `go test ./...` и закоммитьте. Колонки увидят изменения при следующем обновлении источников.
