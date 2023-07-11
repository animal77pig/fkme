set GOARCH=386

set GOOS=linux
go build -ldflags "-s -w"

set GOOS=windows
go build -ldflags "-s -w"
mv fkme.exe \devtool\bin\

rem fkme scp -daemon -s5 127.0.0.1:31080 -f ~ fkme ud7:gosrc/fkme
