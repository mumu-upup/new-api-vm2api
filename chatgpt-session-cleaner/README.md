# ChatGPT 会话清理扩展

这是手动浏览器登录路径的本地 Chrome/Chromium 扩展。加载后，它只监听
`http://localhost:1455/auth/callback`，并在收到包含 `code` 与 `state` 的回调后，
打开 ChatGPT 的“设置 → 安全性 → 活动会话”。扩展只点击明确标记为其他设备的单项
“Log out”，始终保留 `CURRENT SESSION`，也不会点击“Log out all”。

安装：打开 `chrome://extensions`，开启“开发者模式”，选择“加载已解压的扩展程序”，
选中本目录。点击扩展图标，先勾选“为下一次登录回调启用会话清理”，完成一次清理后该开关会自动关闭。这样其他本机 OAuth 回调不会意外触发退出操作。
