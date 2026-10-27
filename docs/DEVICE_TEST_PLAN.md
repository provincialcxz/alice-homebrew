# Проверка 0.7.0 на Мини 1

Чек-лист для первой проверки на колонке. Отмечайте пункты и присылайте всё, что отмечено 📋: вывод команд, коды кнопок, значения датчика. Ничего из этого не содержит личных данных.

Все команды — в PowerShell на ПК, где ставился root, колонка подключена по USB в обычном режиме ADB. Сначала один раз в окне PowerShell:

```powershell
$env:ADB_VENDOR_KEYS = "$env:LOCALAPPDATA\StationMiniRoot\work\keys\adbkey"
adb devices
```

## 0. Поставить сборку 0.7.0 без релиза

До публикации релиза менеджер ставится вручную. Сборка не трогает плагины и их настройки; откат — шаг 0.4.

1. Собрать (в папке форка `alice-homebrew`):
   ```powershell
   $env:CGO_ENABLED="0"; $env:GOOS="linux"; $env:GOARCH="arm64"; go build -trimpath -ldflags="-s -w" -o alice-homebrew-linux-arm64 ./cmd/alice-homebrew; Remove-Item Env:GOOS,Env:GOARCH,Env:CGO_ENABLED
   ```
2. Скопировать на колонку вместе с новым скриптом автозапуска:
   ```powershell
   adb push alice-homebrew-linux-arm64 /data/alice-homebrew/alice-homebrew.new
   adb push scripts/start-speaker.sh /data/alice-homebrew/start.sh.new
   ```
3. Заменить, сохранив прежнюю версию для отката:
   ```powershell
   adb shell "cd /data/alice-homebrew && ./start.sh stop && cp alice-homebrew alice-homebrew.before-0.7.0 && chmod 700 alice-homebrew.new start.sh.new && mv alice-homebrew.new alice-homebrew && mv start.sh.new start.sh && ./start.sh start && sleep 2 && tail -5 service.log"
   ```
4. Откат, если что-то пошло не так:
   ```powershell
   adb shell "cd /data/alice-homebrew && ./start.sh stop && cp alice-homebrew.before-0.7.0 alice-homebrew && ./start.sh start"
   ```

- [ ] Страница `http://IP-КОЛОНКИ:8088` открывается, в «Настройки → Устройство» версия `0.7.0`.

## 1. Безопасность

- [ ] Защита от DNS rebinding — чужое имя отклоняется, IP пропускается:
  ```powershell
  curl.exe -s -o NUL -w "%{http_code}`n" -H "Host: evil.example:8088" http://IP-КОЛОНКИ:8088/api/state
  curl.exe -s -o NUL -w "%{http_code}`n" http://IP-КОЛОНКИ:8088/api/state
  ```
  Ожидается `403`, затем `200`.
- [ ] Код доступа: `adb shell "touch /data/alice-homebrew/access-code.enabled && /data/alice-homebrew/start.sh stop && /data/alice-homebrew/start.sh start && cat /data/alice-homebrew/state/admin-token.txt"`. Страница просит код, код подходит, «Выйти» выходит.
- [ ] Вход сохраняется после перезапуска службы (`start.sh stop` и `start`) — не просит код снова.
- [ ] Снять код: `adb shell "rm /data/alice-homebrew/access-code.enabled && /data/alice-homebrew/start.sh stop && /data/alice-homebrew/start.sh start"`.

## 2. Кнопки, датчик и Алиса — «Настройки → Диагностика кнопок и датчика»

- [ ] Строки «Кнопки» и «Датчик» показывают `ready`. Если там ошибка — 📋 пришлите её.
- [ ] 📋 Нажмите по очереди каждую кнопку коротко и с удержанием 3 секунды. Пришлите список «код → какая кнопка» и что при этом сделала Алиса.
- [ ] 📋 Расстояние: значения без руки, ладонь на 20 см, на 5 см, вплотную. Меняется ли оно вообще?
- [ ] «Алиса» показывает «тишина», при «Алиса…» — «слушает», затем «думает» и «говорит». 📋 Если всегда «нет связи с событиями», пришлите вывод `adb shell "netstat -ltn | grep 9875"`.

## 3. Подтверждение кнопкой

- [ ] Включить: `adb shell "touch /data/alice-homebrew/button-confirm.enabled && /data/alice-homebrew/start.sh stop && /data/alice-homebrew/start.sh start"`.
- [ ] Выключить и снова включить нативный плагин (например, AirPlay): появляется «Нажмите любую кнопку…», кольцо мигает янтарным, после нажатия плагин включается.
- [ ] Без нажатия через 30 секунд — ошибка, плагин остаётся выключенным.

## 4. Плагины без root из каталога provincialcxz

Каталог появится после слияния в `main`. До этого установите пакеты вручную кнопкой «Добавить пакет»: соберите их командой `go run ./cmd/alice-homebrew pack examples/ring-clock RingClock.aliceplugin` (и так же `examples/sound-ring`).

- [ ] «Часы на кольце»: задайте свой часовой пояс, сверьте час и минуты с реальным временем. 📋 Хватает ли яркости минутного огонька (35 %)?
- [ ] «Звуковое кольцо»: включите музыку через AirPlay — кольцо реагирует. Кнопка переключает спектр и пульс. Ладонь над колонкой приглушает кольцо; 📋 подходит ли порог 100 или какое значение лучше по пункту 2.

## 5. Spotify Connect

Пакет собирает CI (`spotify.yml`, артефакт `spotify-connect-plugin`) или вы сами на Linux/WSL: `bash plugins/spotify/build-runtime.sh && python3 plugins/spotify/build-plugin.py`. Установка — «Добавить пакет», файл `release/spotify-connect-2.0.0-beta.1.aliceplugin`. Нужны Spotify Premium и свой ключ Soloist (см. README плагина).

Перед включением:

```bash
adb shell "df -h /data; free -m"
```

📋 Свободное место на `/data` (плагину нужно около 100 МБ) и свободная память.

- [ ] В настройках плагина вставьте ключ и сохраните: поле показывает «Ключ сохранён на колонке», справа «API-ключ: сохранён».
- [ ] Включите плагин. В журнале `downloaded Soloist …`, затем `using Soloist …, expires …` и `Spotify Connect ready`.
- [ ] Колонка видна в Spotify, вход проходит, музыка играет, Алиса продолжает отвечать.
- [ ] 🎧 Звук чистый, без заиканий и треска, громкость из приложения Spotify меняется.
- [ ] Во время музыки скажите «Алиса» — музыка плавно стихает до 20 % и возвращается после ответа.
- [ ] «Звуковое кольцо» реагирует и на Spotify.
- [ ] После перезапуска службы колонка остаётся в аккаунте Spotify.
- [ ] 📋 Через 10 минут музыки: `adb shell "top -b -n 1 | head -15; du -sh /data/alice-homebrew/plugins/spotify-connect/*"` — нагрузка на процессор, память Soloist и размер кэша (не больше ~24 МБ).
- [ ] 📋 Если что-то не так — строки журнала плагина с `error`, `alice-pulse` или `Soloist`.

## 6. Разведка для громкости в API 3

📋 Пришлите вывод — по нему сделаю чтение громкости Алисы:

```powershell
adb shell "cat /proc/asound/cards; ls /system/workdir/bin | grep -i -E 'amixer|alsa'; ls /usr/bin /bin | grep -i amixer"
adb shell "LD_LIBRARY_PATH=/system/vendor/quasar/libs:/system/workdir/lib /system/workdir/bin/amixer -c 0 scontrols 2>&1 | head -20"
```

Поменяйте громкость голосом («Алиса, громкость 3», затем 7) и после каждой повторите вторую команду с `sget Master` вместо `scontrols`.

## 7. Вернуть как было

```powershell
adb shell "cd /data/alice-homebrew && rm -f access-code.enabled button-confirm.enabled && ./start.sh stop && cp alice-homebrew.before-0.7.0 alice-homebrew && ./start.sh start"
```

Скрипт автозапуска 0.7.0 совместим с прежним менеджером, пока нет файлов-меток.
