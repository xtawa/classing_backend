# Classing 管理台品牌规范（Material You）

## 品牌资产

- 应用图标：`web-v0/assets/classing-icon.png`
- 资产来源：`F:\data\WearOS_ClassingTimeTable\mobile\src\main\res\drawable\ic_launcher_foreground.png`
- 使用规则：应用图标只以图片形式引用，不使用 CSS 或手绘 SVG 重新绘制。

## 产品定位

- 叙事角色：Classing 后台服务的运营控制台与用户自助管理中心。
- 主要设备：桌面浏览器；平板和手机提供响应式降级。
- 视觉温度：友好、可信、带有 Material You 的个性化色彩与柔和形状。
- 信息容量：管理员使用高密度表格和批量操作；普通用户使用更轻量的卡片与课表视图。

## 设计令牌

### Material 3 色彩角色

- Seed / Primary：`#375F91`
- On primary：`#FFFFFF`
- Primary container：`#D2E4FF`
- On primary container：`#0D3159`
- Secondary：`#4E616F`
- Secondary container：`#D2E5F5`
- Tertiary：`#63597C`
- Tertiary container：`#E9DDFF`
- Surface：`#F8F9FF`
- Surface container low：`#F2F3FA`
- Surface container：`#ECEEF5`
- Surface container high：`#E6E8EF`
- Error：`#BA1A1A`

颜色只通过语义角色使用。Classing Blue、Mint 和 Sunrise 三套预览方案共用相同的角色映射。

### 字体

- 标题：`Google Sans`, `Noto Sans SC`, `Microsoft YaHei`, sans-serif
- 正文：`Noto Sans SC`, `Microsoft YaHei`, sans-serif
- 等宽：`JetBrains Mono`, `Cascadia Code`, monospace

### 形状与间距

- 基准网格：4px
- 常用间距：8 / 12 / 16 / 24 / 32px
- 控件圆角：16px 或全圆角
- 小型卡片圆角：20px
- 主要面板圆角：28px
- Hero 容器圆角：32px

### 层级与动效

- 常规层级由 `surface-container` 色调差区分，尽量不依赖描边。
- 只有弹出菜单、对话框、FAB 和浮动工具使用阴影。
- 微交互时长：140ms；页面和面板切换：220ms。
- 标准缓动：`cubic-bezier(.2,.8,.2,1)`。
- 遵循 `prefers-reduced-motion`。
