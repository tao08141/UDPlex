### 协议头
Version (1字节)：协议版本，当前为1
MsgType (1字节)：消息类型
Reserved (2字节)：保留字段
Length (2字节)：数据部分长度（大端序）
Data (Length字节)：数据部分，具体内容根据MsgType而定


### 消息类型
类型值	消息类型	描述
1	AuthChallenge	认证挑战
2	AuthResponse	认证响应
4	Heartbeat	心跳包
5	Data	数据包
6	Disconnect	断开连接


### 认证流程
1. 客户端发送AuthChallenge消息，包含一个随机数challenge与时间戳，使用HMAC-SHA256加密。
   - challenge (32字节)：随机数
   - timestamp (8字节)：时间戳，单位为毫秒
   - mac (32字节)：使用HMAC-SHA256加密的结果
2. 服务器收到AuthChallenge后，认证成功则返回响应，认证失败则直接丢弃数据，没任何响应，避免在互联网中被探测。
3. 服务器认证需要判断timestamp是否在合理范围内（如5分钟内），mac是否正确。
3. 服务器认证成功后，生成一个随机数response，并使用HMAC-SHA256加密challenge和response。
    - response (32字节)：服务器生成的随机数
    - timestamp (8字节)：时间戳，单位为毫秒
    - mac (32字节)：使用HMAC-SHA256加密的结果
4. 客户端收到AuthResponse后，验证mac是否正确，若正确则认证成功。


### 心跳包
心跳包用于保持连接活跃，客户端和服务器可以定期发送心跳包以确认连接状态。
1. 客户端发送Heartbeat消息，不包含任何数据。
2. 服务器收到Heartbeat后，直接返回一个相同的Heartbeat消息。
3. 若客户端长时间未发送心跳包，服务器可以主动断开连接。
4. 客户端连续发送心跳包超过一定次数（如3次）未收到服务器响应，超时n秒(如5s)开始发送AuthChallenge，直到收到服务器响应或超过最大重试次数（如5次）后开始重连。

### 数据包
数据包用于传输实际的数据内容，客户端和服务器可以互相发送数据包。
1. 客户端发送Data消息，包含实际数据内容，如果启动了加密，则数据内容需要使用AES-128-GCM加密。
未加密的数据包格式如下：
   - data (Length字节)：实际数据内容
加密的数据包格式如下：
    - Nonce (12字节)：AES-128-GCM的初始化向量
    - Timestamp (8字节)：时间戳，单位为毫秒，用于防止重放攻击，Timestamp需要与data一起加密
    - data (Length-20字节)：实际数据内容
2. 收到数据后需要判断timestamp是否在合理范围内（如30s内），如果超时则丢弃数据包。
