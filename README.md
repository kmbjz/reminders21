build:
```bash
env GOOS=linux GOARCH=amd64 CGO_ENABLED=1 go build -o bin/ .
```

deploy:
```bash
scp bin/reminders21 root@164.90.169.*:~/reminders21/
```

broadcast:
```
./reminders21 -broadcast -all
```

```
./reminders21 -broadcast -chat=123456789 -message="Important announcement: Bot will be down for maintenance tomorrow from 2-3 PM."
```

```
echo "Hello everyone!" | ./reminders21 -broadcast -all
```

```
./reminders21 -broadcast -all
# Then type your message and press Ctrl+D when finished
```