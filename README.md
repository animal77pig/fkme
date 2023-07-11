Fork me
=========
    An auto sync the changes dir/file to remote server tool via ssh/sftp.

Wiki
----
    golang 实现的一些工具集合,  包括通过sftp同步文件, socks5代理, 静态文件web服务, 后台进程管理等

前置环境
-----
    golang

支持平台
-----
    Linux/Windows

使用方法
-----
    1. go get -v -u -x github.com/lulugyf/fkme
    2. cd fkme && go build

    fkme -h 获取帮助
运行
-----
    1. run "file-sync" or "file-sync.exe"(windows) 默认读取config.json配置启动
    2. run "file-sync -config=filename" or "file-sync.exe -config=filename" 读取指定filename文件作为配置启动

Tips
-----
    1. go get过程中出现package golang.org/x/sys/unix: unrecognized import path "golang.org/x/sys/unix"报错,解决方案参考:
       https://javasgl.github.io/go-get-golang-x-packages/
    2. 由于fsnotify库实现问题，本工具只监听:文件/目录的create write remove事件(放弃rename事件监听)，创建文件/目录,修改文件,删除文件或目录操作均可自动同步远端.
       但文件/目录重命名仅会自动同步更名后的目标到远端，旧的文件/目录不会自动从远端移除，有需要可通过终端remove命令手动移除