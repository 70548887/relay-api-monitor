# 1.1.0 实际接口验证

使用用户提供的地址和Key进行显式调用，Key仅经stdin进入本地配置工具并加密保存。地址和Key不进入发布源码或验证报告；调用记录时间为UTC，界面统一显示北京时间。

- 模型目录：实际读取26个模型，选择`gpt-6.1-sol`。
- Responses短文本：显式High，HTTP200，完整返回`OK`；耗时4102ms，上游返回总用量4232。
- Chat短文本：显式High，HTTP200，完整返回`OK`；耗时1559ms，上游返回总用量16。
- 中文模板：原文“创建一个HTML，内容是SVG绘一个鹈鹕骑自行车的2D动画”；成功作品`832b1c7b33ca41c176e7f79714e75d6a`，实际High；208372ms，首正文6326ms，接收16345字符；输入4242、输出7126。浏览器已确认实际SVG具有运行中的CSS动画。
- 英文模板：原文“Create code for an SVG of a pelican riding a bicycle as nicely as you can.”；成功作品`167c8349660bf973b98c1dee531ceed6`，实际High；143027ms，首正文38938ms，接收10231字符；输入4240、输出4556。
- 监控任务：HTTP200，4348ms，首正文4318ms，完整成功，已写入真实监控结果。
- 在线测试模型读取：通过本地`/api/v1/test/models`实际转发，返回26个模型，接口耗时约218ms。
- 在线测试流式动画：通过本地`/api/v1/test/generate`使用Responses High和中文模板，HTTP200；收到meta、约6006段delta、1个artifact和done事件，正文累计约17808字符。访客测试结果没有写入公开作品历史。
- 在线测试页面：在`http://127.0.0.1:42783/test`实际填写地址、Key并读取模型，选择`gpt-6.1-sol`完成Chat文本测试；HTTP200，首字约2360ms，总耗时约3700ms，输入13、输出48 tokens，返回模型为`gpt-6.1-sol`。
- Anthropic实际模型目录：通过本地`/api/v1/test/models`使用`discovery_protocol=anthropic`读取17个模型，选择`claude-sonnet-5-5`。
- Anthropic Messages文本：通过本地`/api/v1/test/generate`，显式High + adaptive thinking，HTTP200，meta/delta/done事件完整，返回`ANTHROPIC_OK`。
- Anthropic中文动画（High + adaptive thinking）：HTTP200并收到HTML正文和artifact事件，但本次流结束没有收到done终态，因此按“不完整响应”处理，不写入公开作品历史。
- Anthropic中文动画复测（High，省略thinking参数）：HTTP200，收到meta、delta、artifact、done完整事件；正文约18911字符，包含HTML/SVG，证明该线路在上游默认thinking配置下可完成作品链路。在线页面默认使用“上游默认”，不会强制adaptive thinking。
- 绑定组核验：标准New API元信息接口返回不支持，页面明确展示unsupported；不据此判断模型Key不可用，也不推测实际分组或账号池。

所有用量均直接采用上游返回值，不自行估算。返回模型字符串为上游标识，不等同模型身份鉴定。Claude映射由本地接口夹具验证，本次接口目录未提供Claude模型，未宣称Claude真实调用通过。

## 浏览器和数据

真实作品只读验收访问本机42783，无生成或管理写请求；检查实际SVG动画、真实PNG封面、完整画面边界及手机布局。由于Chrome在滚动后的首次截图可能尚未完成iframe绘制，截图验收需等待两个animation frame并检查场景像素，不以单纯“元素存在”作为完整显示依据。

本机保留一条实测线路、26条实际模型目录、两份成功作品与一条成功监控结果。循环付费任务保持默认关闭，管理员可自行开启。源码包与成品包不包含本机data、数据库和主密钥。

验证明细在本机`release/validation/live-*.json`，实际作品截图在`release/screenshots/intelligence-live-1.1.0.png`。
