reboot 逻辑问题：
NIR 自定义资源中的 count字段存在更新会滞后，比如：
第一次发生 OOMKill 的时候 count 数为 1
第二次发生 OOMKill 的时候，count 数仍为 1
第三次发生 OOMKill 的时候， count 数为 2
认为可能是由于 Lister 的更新延迟导致。


其余功能正常。

kuernetes 中会将旧的 Events 在集群中保存一段时间，可能会造成旧的 events 被多次处理，需要过滤事件，只记录 eventcontroller 开始之后的事件
应该添加 eventcontroller 的 filter，过滤掉旧的events。

当前测试 replace 逻辑也正常工作
