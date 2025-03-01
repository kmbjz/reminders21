build:
```bash
env GOOS=linux GOARCH=amd64 CGO_ENABLED=1 go build -o bin/ .
```

deploy:
```bash
scp bin/reminders21 root@164.90.169.*:~/reminders21/
```