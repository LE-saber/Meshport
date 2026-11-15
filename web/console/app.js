/* Live console logic for the original Meshport Design template.
 * Auth uses the server's HttpOnly cookie; secrets never enter localStorage.
 */
class Component extends DesignBase {
  componentDidMount() {
    this._data = {};
    this._epoch = 0;
    this._alive = true;
    this.restore();
    this._poll = setInterval(() => { if (this.g('me', null)) this.refresh(); }, 15000);
    this._pop = () => this.go(location.hash.slice(1) || 'overview');
    window.addEventListener('hashchange', this._pop);
  }
  componentWillUnmount() {
    this._alive = false;
    clearInterval(this._poll);
    clearTimeout(this._refreshTimer);
    clearTimeout(this._reconnect);
    clearTimeout(this._toastT);
    this._socket?.close();
    window.removeEventListener('hashchange', this._pop);
  }
  async request(path, method = 'GET', body) {
    const r = await fetch('/api/v1' + path, {
      method, credentials: 'same-origin', cache: 'no-store',
      headers: { 'X-Requested-With': 'MeshportConsole', ...(body === undefined ? {} : { 'Content-Type': 'application/json' }) },
      body: body === undefined ? undefined : JSON.stringify(body),
      signal: AbortSignal.timeout(45000)
    });
    const content = await r.text();
    let data;
    try { data = content ? JSON.parse(content) : {}; } catch { data = content; }
    if (!r.ok) {
      const e = new Error(data?.error?.message || `HTTP ${r.status}`);
      e.status = r.status;
      e.code = data?.error?.code;
      if (r.status === 401 && path !== '/auth/login' && path !== '/auth/password' && !path.startsWith('/auth/mfa')) this.expired();
      throw e;
    }
    return data;
  }
  expired() {
    this._epoch++;
    this._data = {};
    clearTimeout(this._reconnect);
    this._socket?.close();
    if (this._alive) this.setState({ me: null, checking: false, data: {}, dialog: null, result: null, overlay: null, draft: {}, error: '会话已过期，请重新登录。' });
  }
  async restore() {
    this.setState({ checking: true });
    try {
      const me = await this.request('/auth/me');
      if (!this._alive) return;
      this.setState({ me, checking: false, error: '', page: location.hash.slice(1) || 'overview', theme: localStorage.getItem('meshport.theme') || me.settings?.theme || 'light', accent: me.settings?.accent || 'jade', lang: (['en','zh','ja'].includes(localStorage.getItem('meshport.legacy.lang')) ? localStorage.getItem('meshport.legacy.lang') : me.settings?.language === 'ja' ? 'ja' : (me.settings?.language === 'zh-TW' || me.settings?.language === 'zh-CN' ? 'zh' : 'en')) });
      this.connectStream();
      await this.refresh();
    } catch (e) { if (e.status !== 401) this.setState({ checking: false, error: e.message }); }
  }
  async login(e) {
    e?.preventDefault();
    if (this.g('busy', false)) return;
    this.setState({ busy: true, error: '' });
    try {
      await this.request('/auth/login', 'POST', { username: this.g('loginUser', 'admin'), password: this.g('loginPassword', ''), totp: this.g('loginTotp', '') });
      this.setState({ loginPassword: '', loginTotp: '' });
      await this.restore();
    } catch (e) { this.setState({ error: e.message, needTotp: e.code === 'mfa_required' || e.code === 'invalid_mfa' }); }
    finally { this.setState({ busy: false }); }
  }
  async logout() {
    try { await this.request('/auth/logout', 'POST', {}); this.expired(); this.setState({ error: '' }); }
    catch (e) { this.setState({ error: e.message }); }
  }
  connectStream() {
    if (!this._alive || !this.g('me', null)) return;
    this._socket?.close();
    const ws = new WebSocket(`${location.protocol === 'https:' ? 'wss:' : 'ws:'}//${location.host}/api/v1/admin/stream`);
    this._socket = ws;
    ws.onopen = () => { if (ws === this._socket) this.setState({ stream: 'live' }); };
    ws.onmessage = e => {
      if (ws !== this._socket) return;
      try {
        const ev = JSON.parse(e.data);
        if (ev.type === 'metrics.tick') {
          this._data.latest = ev.data;
          this.setState({ data: { ...this._data } });
        } else if (ev.type !== 'hello') {
          clearTimeout(this._refreshTimer);
          this._refreshTimer = setTimeout(() => this.refresh(), 600);
        }
      } catch { /* The next poll still recovers the view. */ }
    };
    ws.onclose = () => {
      if (ws !== this._socket || !this._alive || !this.g('me', null)) return;
      this.setState({ stream: 'polling' });
      this._reconnect = setTimeout(() => this.connectStream(), 5000);
    };
    ws.onerror = () => ws.close();
  }
  page() { return ['overview','devices','comms','frp','access','security','monitor','system'].includes(this.g('page', 'overview')) ? this.g('page', 'overview') : 'overview'; }
  go(page) {
    this.setState({ page, overlay: null, dialog: null, q: '', error: '' });
    if (location.hash !== '#' + page) history.replaceState(null, '', '#' + page);
    this.refresh(page);
  }
  async refresh(page = this.page()) {
    if (!this.g('me', null) || this._refreshing) { this._refreshAgain = !!this.g('me', null); return; }
    this._refreshing = true;
    const epoch = this._epoch;
    const common = { overview:'/admin/overview', devices:'/admin/devices', nodes:'/admin/frp/nodes', mappings:'/admin/frp/mappings', grants:'/admin/grants', settings:'/admin/system/settings', status:'/admin/system/status', alerts:'/admin/alerts' };
    const byPage = {
      overview: { p2p:'/admin/metrics/p2p' }, devices: { groups:'/admin/groups', users:'/admin/users', enroll:'/admin/enroll-tokens' },
      comms: { sessions:'/admin/sessions?include_closed=1' },
      frp: { clients:'/admin/frp/clients' },
      access: { users:'/admin/users', levels:'/admin/levels', groups:'/admin/groups' },
      security: { users:'/admin/users', logins:'/admin/security/logins', keys:'/admin/security/keys', mfa:'/admin/security/mfa', tokens:'/admin/security/tokens', acl:'/admin/security/acl', bans:'/admin/security/bans' },
      monitor: { metrics:`/admin/metrics?range=${this.g('range','24h')}&points=120`, p2p:'/admin/metrics/p2p', failures:'/admin/failures', logs:'/admin/logs?limit=200', audit:'/admin/audit?limit=200' },
      system: { db:'/admin/system/db?check=1', backups:'/admin/system/backups', versions:'/admin/system/versions', hooks:'/admin/system/webhooks', endpoints:'/admin/system/endpoints', update:'/admin/system/update' }
    };
    if (this.g('me', null)?.role === 'admin' && page === 'system') byPage.system.apiKeys = '/admin/system/api-keys';
    const entries = Object.entries({ ...common, ...(byPage[page] || {}) });
    try {
      const results = await Promise.allSettled(entries.map(([,url]) => this.request(url)));
      if (epoch !== this._epoch || !this._alive) return;
      const errors = [];
      results.forEach((r,i) => {
        const [key, url] = entries[i];
        if (r.status === 'fulfilled') this._data[key] = key === 'settings' ? r.value.settings : r.value;
        else if (r.reason.status !== 401) errors.push(`${url}: ${r.reason.message}`);
      });
      this.setState({ data: { ...this._data }, loadErrors: errors, loadedAt: new Date().toLocaleTimeString() });
    } finally {
      this._refreshing = false;
      if (this._refreshAgain) { this._refreshAgain = false; this.refresh(); }
    }
  }
  async change(path, method = 'POST', body = {}, options = {}) {
    if (this.g('busy', false)) return;
    this.setState({ busy: true, error: '', dialogError: '' });
    try {
      const value = await this.request(path, method, body);
      if (options.close !== false) this.setState({ dialog: null, overlay: null, draft: {} });
      if (options.result) this.showResult(options.result, value);
      this.flash('操作成功');
      await this.refresh();
      if (options.me) { const me = await this.request('/auth/me'); this.setState({ me }); }
      return value;
    } catch (e) { this.setState({ error: e.message, dialogError: e.message }); return undefined; }
    finally { this.setState({ busy: false }); }
  }
  confirmAction(title, fn, body = '此操作会立即影响服务端的真实数据。') {
    this._cfn = fn;
    this.setState({ cf: { title, body, ok: '确认执行' } });
  }
  form(title, fields, submit, initial = {}) {
    this._submit = submit;
    this.setState({ dialog: { title, fields }, draft: { ...initial }, dialogError: '', result: null, manager: null });
  }
  async submitForm(e) {
    e?.preventDefault();
    if (this.g('busy', false)) return;
    try {
      const d = this.g('draft', {});
      const out = {};
      for (const f of this.g('dialog', {fields:[]}).fields) {
        const v = d[f.key];
        if (f.required && (v === undefined || String(v).trim() === '')) throw new Error(`请填写${f.label}`);
        if (f.omitEmpty && (v === '' || v === undefined)) continue;
        out[f.key] = f.type === 'number' ? Number(v ?? f.default ?? 0) : f.type === 'checkbox' ? !!v : f.type === 'list' ? String(v || '').split(',').map(x => x.trim()).filter(Boolean) : v ?? '';
      }
      await this._submit(out);
    } catch (e) { this.setState({ dialogError: e.message }); }
  }
  showResult(title, value) { this.setState({ result: { title, text: typeof value === 'string' ? value : JSON.stringify(value, null, 2) }, dialog: null, manager: null }); }
  async copy(text) { try { await navigator.clipboard.writeText(text); this.flash('已复制'); } catch { this.flash('请选中文本后手动复制。'); } }
  download(path, name) { const a = document.createElement('a'); a.href = '/api/v1' + path; if (name) a.download = name; a.click(); }
  async exportAudit() {
    try {
      const data = await this.request('/admin/audit?limit=2000');
      const url = URL.createObjectURL(new Blob([JSON.stringify(data, null, 2)], {type:'application/json'}));
      const a = document.createElement('a'); a.href=url; a.download='meshport-audit.json'; a.click(); setTimeout(() => URL.revokeObjectURL(url), 1000);
    } catch(e) { this.setState({error:e.message}); }
  }
  fields(names) {
    const labels = { name:'名称', note:'备注', user:'所属用户', group:'分组', level:'设备等级', uses:'可使用次数', ttl_hours:'有效时间（小时）', auto_approve:'自动批准设备注册', username:'用户名', password:'密码（留空则自动生成）', display_name:'显示名称', email:'电子邮箱', role:'控制台角色', disabled:'停用', target:'目标设备', reason:'原因', duration:'有效期', public_addr:'公网地址', bind_port:'frpc 连接端口', vhost_http_port:'HTTP 代理端口', vhost_https_port:'HTTPS 代理端口', dashboard_addr:'Dashboard 地址', dashboard_user:'Dashboard 用户名', dashboard_password:'Dashboard 密码', managed:'本机托管 frps', enabled:'启用', allow_ports:'允许映射端口范围', subdomain_host:'子域名根域', region:'区域', scopes:'权限范围（逗号分隔）', ttl_days:'有效天数（0 为永久）', url:'Webhook URL', events:'订阅事件（逗号分隔）', current:'当前密码', new:'新密码', code:'验证器验证码', kind:'设备类型' };
    const enums = {level:['guest','member','admin'], role:['viewer','operator','admin','user'], duration:['24h','7d','30d','90d','perm'], kind:['desktop','laptop','phone','tablet','server','nas','other']};
    const lists = {user:this.items('users').map(x=>[x.id,x.username]), group:this.items('groups').map(x=>[x.id,x.name]), target:this.items('devices').map(x=>[x.id,x.name])};
    return names.map(key => ({key, label:labels[key] || key, type: enums[key] || lists[key] ? 'select' : ['managed','enabled','disabled','auto_approve'].includes(key) ? 'checkbox' : ['uses','ttl_hours','ttl_days','bind_port','vhost_http_port','vhost_https_port'].includes(key) ? 'number' : ['password','current','new','dashboard_password'].includes(key) ? 'password' : ['scopes','events'].includes(key) ? 'list' : 'text', options: enums[key]?.map(x=>[x,x]) || lists[key], required:['name','username','target','public_addr','current','new','code'].includes(key), omitEmpty:['password','dashboard_password','user','group'].includes(key)}));
  }
  items(key) { const d = this.g('data', {})[key]; return Array.isArray(d) ? d : Array.isArray(d?.items) ? d.items : []; }
  enroll() { this.form('注册新装置 · 生成注册码',this.fields(['note','user','group','level','uses','ttl_hours','auto_approve']),d=>this.change('/admin/enroll-tokens','POST',d,{result:'注册码（仅显示一次，请保存）'}),{level:'guest',uses:1,ttl_hours:24,auto_approve:false}); }
  userForm(user) {
    this.form(user?'编辑用户':'新增用户',this.fields(user?['display_name','email','role','disabled','password']:['username','display_name','email','role','password']),d=>this.change('/admin/users'+(user?'/'+encodeURIComponent(user.id):''),user?'PATCH':'POST',d,{result: user ? null : '用户创建结果'}),user || {role:'viewer'});
  }
  groupForm(group) { this.form(group?'编辑分组':'新增分组',this.fields(['name','note']).map(x=>x.key==='note'?{...x,key:'description',label:'说明'}:x),d=>this.change('/admin/groups'+(group?'/'+encodeURIComponent(group.id):''),group?'PATCH':'POST',d),group||{}); }
  nodeForm(node) {
    const fields = this.fields(['name','region','public_addr','bind_port','vhost_http_port','vhost_https_port','allow_ports','subdomain_host','dashboard_addr','dashboard_user','dashboard_password','managed','enabled']);
    this.form(node?'编辑 frps 节点':'新增 frps 节点',fields,d=>this.change('/admin/frp/nodes'+(node?'/'+encodeURIComponent(node.id):''),node?'PATCH':'POST',d,{result:node?null:'节点配置（包含凭据，请妥善保管）'}),node||{bind_port:7002,vhost_http_port:0,vhost_https_port:0,allow_ports:'20000-29999',dashboard_addr:'127.0.0.1:7502',managed:false,enabled:true});
  }
  grantForm() { this.form('直接授予设备等级',this.fields(['target','level','reason','duration']),d=>this.change('/admin/grants','POST',d),{level:'guest',duration:'7d'}); }
  tokenForm(kind='token') { this.form(kind==='key'?'创建服务 API 密钥':'创建个人 API 令牌',this.fields(['name','scopes','ttl_days']),d=>this.change(kind==='key'?'/admin/system/api-keys':'/admin/security/tokens','POST',d,{result:'新令牌（仅显示一次，请保存）'}),{scopes:'read',ttl_days:30}); }
  hookForm(hook) { this.form(hook?'编辑 Webhook':'新增 Webhook',this.fields(['name','url','events','enabled']),d=>this.change('/admin/system/webhooks'+(hook?'/'+encodeURIComponent(hook.id):''),hook?'PATCH':'POST',d,{result:hook?null:'Webhook 签名密钥（仅显示一次）'}),hook?{...hook,events:hook.events.join(',')}:{events:'device.*,alert.*',enabled:true}); }
  account() { this.form('修改自己的密码',this.fields(['current','new']),d=>this.change('/auth/password','POST',d)); }
  async mfaSetup() {
    try { const v=await this.request('/auth/mfa/setup','POST',{}); this.showResult('添加到验证器后，点击「确认启用 MFA」输入验证码',v); }
    catch(e) { this.setState({error:e.message}); }
  }
  mfaConfirm(disable=false) { this.form(disable?'停用自己的 MFA':'确认启用 MFA',this.fields(['code']),d=>this.change('/auth/mfa/'+(disable?'disable':'enable'),'POST',d,{me:true})); }
  deviceEdit(d) { this.form('编辑装置',this.fields(['name','kind','level','group','user']).map(f=>({...f,omitEmpty:false})),v=>this.change('/admin/devices/'+encodeURIComponent(d.id),'PATCH',v),{...d,group:d.group_id,user:d.user_id}); }
  settingsForm() {
    const settings=this.g('data',{}).settings || {};
    const labels={console_name:'控制台名称',open_registration:'开放设备注册',registration_approval:'注册审批',default_level:'默认设备等级',discovery_scope:'设备可见范围',mfa_required:'强制 MFA',relay_enabled:'允许消息与文件中继',relay_max_file_mb:'中继文件上限（MB）',message_retention_days:'消息保存天数',file_retention_hours:'文件保存小时数',log_retention_days:'日志保存天数',backup_hour:'备份时间（UTC 小时，-1 禁用）',backup_keep:'备份保留份数',update_channel:'更新频道',auto_update:'自动更新',session_hours:'管理会话有效小时数',device_token_hours:'设备令牌有效小时数',abnormal_fail_limit:'异常登录失败阈值',theme:'主题',accent:'强调色',language:'语言'};
    const enums={registration_approval:['manual','auto'],default_level:['guest','member','admin'],discovery_scope:['all','group','user'],update_channel:['stable','beta'],theme:['light','dark','system'],accent:Object.keys(this.accents()),language:['zh-TW','ja','en']};
    const fields=Object.keys(settings).map(key=>({key,label:labels[key]||key,type:enums[key]?'select':typeof settings[key]==='boolean'?'checkbox':typeof settings[key]==='number'?'number':'text',options:enums[key]?.map(x=>[x,x])}));
    this.form('系统设置',fields,d=>this.change('/admin/system/settings','PATCH',d),settings);
  }
  date(v) { if (!v || String(v).startsWith('0001-')) return '—'; const d=new Date(typeof v==='number'?v*1000:v); return Number.isNaN(d.getTime())?'—':d.toLocaleString(); }
  bytes(v=0) { const units=['B','KiB','MiB','GiB','TiB']; let n=Number(v)||0,i=0;while(Math.abs(n)>=1024&&i<4){n/=1024;i++;}return `${n.toFixed(i?1:0)} ${units[i]}`; }
  duration(v=0) { v=Math.floor(v);return v>=86400?`${Math.floor(v/86400)}d ${Math.floor(v%86400/3600)}h`:v>=3600?`${Math.floor(v/3600)}h ${Math.floor(v%3600/60)}m`:`${Math.floor(v/60)}m`; }
  renderVals() {
    const set=o=>this.setState(o), me=this.g('me',null), data=this.g('data',{}), busy=this.g('busy',false);
    const dark=this.g('theme','light')==='dark'||(this.g('theme','light')==='system'&&matchMedia('(prefers-color-scheme: dark)').matches);
    const lang=this.g('lang','en'), s=this.dict()[lang]||this.dict().en, c=this.pal(dark,this.g('accent','jade')), I=this.icons(), page=this.page();
    const st=(k,label)=>({stLabel:label,stFg:c[k]||c.muted,stBg:c[k+'Soft']||c.tile});
    const seg=(opts,key,def,fn)=>opts.map(([v,label])=>({label,pressed:String(this.g(key,def)===v),bg:this.g(key,def)===v?c.segOn:'transparent',fg:this.g(key,def)===v?c.text:c.muted,sh:'none',pick:()=>{set({[key]:v});fn?.(v);}}));
    const sw=(value,toggle,label)=>({checked:String(!!value),toggle:()=>{if(!busy)toggle();},label,swBg:value?c.accentBg:c.swOff,knobX:value?18:0});
    const common={s,c,I,font:'Inter, "Microsoft JhengHei", system-ui, sans-serif',langTag:{zh:'zh-Hant-TW',ja:'ja',en:'en'}[lang],authenticated:!!me,checking:this.g('checking',true),busy,loginUser:this.g('loginUser','admin'),loginPassword:this.g('loginPassword',''),loginTotp:this.g('loginTotp',''),setLoginUser:e=>set({loginUser:e.target.value}),setLoginPassword:e=>set({loginPassword:e.target.value}),setLoginTotp:e=>set({loginTotp:e.target.value}),login:e=>this.login(e),error:this.g('error',''),hasError:!!this.g('error',''),logout:()=>this.logout(),closeResult:()=>set({result:null}),hasResult:!!this.g('result',null),result:this.g('result',{title:'',text:''}),copyResult:()=>this.copy(this.g('result',{}).text||''),hasToast:!!this.g('toast',''),toast:this.g('toast','')};
    if (!me) return common;
    const canWrite=['operator','admin'].includes(me.role), canAdmin=me.role==='admin';
    const id=v=>encodeURIComponent(v), act=(path,method='POST',body={})=>()=>this.change(path,method,body);
    const remove=(path,title)=>()=>this.confirmAction(title,()=>this.change(path,'DELETE'));
    const level=l=>({lvLabel:{guest:s.lvGuest,member:s.lvMember,admin:s.lvAdmin}[l]||l,lvFg:l==='admin'?'#fff':l==='member'?c.accent:c.muted,lvBg:l==='admin'?c.accentBg:l==='member'?c.accentSoft:c.tile});
    const devices=this.items('devices'), maps=this.items('mappings'), nodes=this.items('nodes'), grants=this.items('grants'), users=this.items('users');
    const overview=data.overview||{}, latest=data.latest||data.metrics?.latest||overview.resources||{}, settings=data.settings||{};
    const nm=v=>devices.find(d=>d.id===v)?.name||v||'—', q=this.g('q','').toLowerCase();
    const matches=x=>!q||JSON.stringify(x).toLowerCase().includes(q);
    const devState=d=>d.status==='active'?(d.online?'online':'offline'):d.status;
    const ds=d=>st(d.status==='active'?(d.online?'ok':'off'):d.status==='pending'?'warn':'crit',({online:s.online,offline:s.offline,disabled:s.disabled,pending:s.pending,banned:'已封禁'})[devState(d)]||d.status);
    const icon=d=>I[{desktop:'monitor',other:'devices',laptop:'laptop',phone:'phone',tablet:'tablet',nas:'nas',server:'server'}[d.kind]]||I.devices;
    const devicePath=d=>'/admin/devices/'+id(d.id);
    const toggleDev=d=>act(devicePath(d)+(d.status==='active'?'/disable':d.status==='banned'?'/unban':'/enable'));
    const detail=d=>{set({overlay:'drawer',drawerId:d.id});this.request(devicePath(d)).then(v=>set({deviceDetail:v})).catch(e=>set({error:e.message}));};
    const devRows=devices.filter(d=>(this.g('devFilter','all')==='all'||devState(d)===this.g('devFilter','all'))&&matches(d)).map((d,i)=>({...ds(d),...level(d.level),name:d.name,did:d.did,os:d.os,ip:d.ip||'—',lan:d.lan_ip||'—',icon:icon(d),user:d.user||'—',group:d.group||'—',last:d.online?s.onlineNow:this.date(d.last_seen_at),div:i?c.hair:'transparent',op:d.status==='active'?1:.6,detail:()=>detail(d),toggle:toggleDev(d),toggleLabel:d.status==='active'?s.disable:d.status==='banned'?'解除封禁':s.enable,toggleIcon:d.status==='active'?I.ban:I.play,toggleFg:d.status==='active'?c.warn:c.ok,remove:remove(devicePath(d),'删除装置 '+d.name+'？')}));
    const counts=data.devices?.counts||{};
    const pending=devices.filter(d=>d.status==='pending');
    const approvals=grants.filter(g=>g.status==='pending');
    const grantAction=(g,action)=>act('/admin/grants/'+id(g.id)+'/'+action);
    const grantState=g=>st(({pending:'warn',approved:'ok',rejected:'crit',revoked:'off',expired:'off'})[g.status]||'off',({pending:s.stPending,approved:s.stApproved,rejected:s.stRejected,revoked:s.stRevoked,expired:s.stExpired})[g.status]||g.status);
    const accRows=grants.filter(g=>this.g('accFilter','pending')==='all'||(this.g('accFilter','pending')==='history'?['rejected','revoked','expired'].includes(g.status):g.status===this.g('accFilter','pending'))).filter(matches).map((g,i)=>({...grantState(g),...level(g.level),app:g.requester,appUser:g.requester,appIcon:I.userCheck,target:g.target,targetId:g.target_id,reason:g.reason,dur:g.duration,until:this.date(g.expires_at),at:this.date(g.created_at),div:i?c.hair:'transparent',isPending:g.status==='pending',isApproved:g.status==='approved',isClosed:!['pending','approved'].includes(g.status),closedBy:g.decided_by,approve:grantAction(g,'approve'),reject:grantAction(g,'reject'),revoke:()=>this.confirmAction('撤销等级授权？',grantAction(g,'revoke'))}));
    const mapState=m=>!m.enabled?'stopped':m.status==='online'?'running':m.status;
    const mapRows=maps.filter(m=>(this.g('frpFilter','all')==='all'||(this.g('frpFilter','all')==='running'?mapState(m)==='running':mapState(m)!=='running'))&&matches(m)).map((m,i)=>({...st(mapState(m)==='running'?'ok':m.enabled?'warn':'off',mapState(m)),name:m.name,client:m.device,server:m.node,proto:m.proto.toUpperCase(),local:m.local_ip+':'+m.local_port,pub:m.public_addr||'—',tin:this.bytes(m.rate_in)+'/s',tout:this.bytes(m.rate_out)+'/s',conns:m.conns,div:i?c.hair:'transparent',edit:()=>this.mapForm(m),toggle:act('/admin/frp/mappings/'+id(m.id)+(m.enabled?'/stop':'/start')),toggleLabel:m.enabled?s.stop:s.start,toggleIcon:m.enabled?I.stop:I.play,toggleFg:m.enabled?c.crit:c.ok,remove:remove('/admin/frp/mappings/'+id(m.id),'删除映射 '+m.name+'？')}));
    const sessions=this.items('sessions'), stateLabel={up:s.stConnected,nego:s.stNego,fail:s.stFailed,closed:s.stClosed,idle:s.stIdle};
    const commRows=sessions.filter(x=>(this.g('commFilter','all')==='all'||x.mode===this.g('commFilter','all'))&&matches(x)).map((x,i)=>({...st(x.state==='up'?'ok':x.state==='fail'?'crit':'off',stateLabel[x.state]||x.state),a:x.a,b:x.b,mode:x.mode,path:x.path,pathFg:x.path==='p2p'?c.ok:c.warn,pathBg:x.path==='p2p'?c.okSoft:c.warnSoft,pathIcon:x.path==='p2p'?I.zap:I.relay,via:x.via||'—',lat:x.latency_ms==null?'—':x.latency_ms+' ms',spd:this.bytes(x.rate_bps)+'/s',msgA:`已发送 ${x.msgs_sent||0} · 送达 ${x.msgs_delivered||0}`,msgB:`已读 ${x.msgs_read||0} · 等待 ${x.msgs_failed||0}`,msgBFg:c.muted,hasFile:!!x.file,noFile:!x.file,fname:x.file?.name,fpct:x.file?.percent||0,fbar:c.accentBg,fsize:this.bytes(x.file?.size),div:i?c.hair:'transparent',cut:()=>this.confirmAction('断开此通信会话？',act('/admin/sessions/'+id(x.id)+'/disconnect')),cutDisabled:String(!canWrite||x.state==='closed'),cutFg:c.text}));
    const alerts=this.items('alerts'), alertSeverity=a=>({fg:c[a.severity==='crit'?'crit':a.severity==='warn'?'warn':'accent'],bg:c[a.severity==='crit'?'critSoft':a.severity==='warn'?'warnSoft':'accentSoft'],icon:a.severity==='crit'?I.critical:a.severity==='warn'?I.alert:I.info,sevLabel:a.severity});
    const alertsTop=alerts.slice(0,5).map(a=>({...alertSeverity(a),title:a.title,src:a.source,t:this.date(a.created_at)}));
    const nodeStats=n=>n.stats||{};
    const chart=(key,sets,percent=false)=>this.chart(key,sets,c,percent);
    const series=data.metrics?.series||{};
    const overviewSeries=overview.series||{};
    const rows=(key)=>series[key]||[];
    const bw=chart('bandwidth',[['P2P',overviewSeries.p2p_bps||[],c.s1],['中继',overviewSeries.relay_bps||[],c.s2]]);
    const cpu=chart('cpu',[['CPU',rows('cpu'),c.bar]],true), mem=chart('mem',[['内存',rows('mem'),c.bar]],true);
    const net=chart('network',[['接收',rows('net_rx_bps'),c.s1],['发送',rows('net_tx_bps'),c.s2]]);
    const rate=chart('p2p',[['P2P 成功率',this.items('p2p').map(x=>({t:new Date(x.hour).getTime()/1000,v:x.rate})),c.s1]],true);
    const relay=chart('relay',[['中继',rows('relay_bps'),c.s2]]), frpChart=chart('frp',[['FRP',rows('frp_bps'),c.bar]]);
    const frpValues=rows('frp_bps'), maxFrp=Math.max(1,...frpValues.map(x=>x.v));
    const frpBars={...frpChart,bars:frpValues.map((x,i)=>({h:x.v/maxFrp*100,color:c.bar,enter:()=>set({hov:{k:'frp',i}})})),total:this.bytes(frpValues.at(-1)?.v||0)+'/s'};
    const resources=[['cpu',s.cpu],['mem',s.memory],['disk',s.disk]].map(([key,label])=>({label,pct:Number(latest[key]??overview.resources?.[key]??0),stLabel:'实时采样',stFg:c.ok,bar:c.bar,detail:key==='cpu'?`load ${latest.load1??overview.resources?.load1??'—'}`:key==='mem'?`${latest.mem_used_mb??'—'} / ${latest.mem_total_mb??'—'} MiB`:`可用 ${latest.disk_free_gb??'—'} GB`}));
    const k=(label,value,unit,sub,ico,target)=>({label,value,unit,sub,subFg:c.muted,icon:ico,iconFg:c.accent,iconBg:c.accentSoft,go:()=>this.go(target)});
    const kpis=[k(s.kOnline,overview.devices?.online??0,' / '+(overview.devices?.total??0),'离线 '+(overview.devices?.offline??0),I.devices,'devices'),k(s.kP2p,overview.sessions?.p2p??0,'','直接连接',I.zap,'comms'),k(s.kRelay,overview.sessions?.relay??0,'','服务器中继',I.relay,'comms'),k(s.kBw,this.bytes((latest.p2p_bps||0)+(latest.relay_bps||0)+(latest.frp_bps||0)),'/s','实际传输速率',I.activity,'monitor'),k(s.kFrp,overview.frp?.active??0,' / '+(overview.frp?.total??0),'节点 '+(overview.frp?.nodes_online??0)+' / '+(overview.frp?.nodes_total??0),I.tunnel,'frp'),k(s.kPend,approvals.length+pending.length,'',`设备 ${pending.length} · 等级 ${approvals.length}`,I.userCheck,'access')];
    const mixCounts=[overview.sessions?.p2p||0,overview.sessions?.relay||0,(overview.sessions?.nego||0)+(overview.sessions?.fail||0)], mixTotal=mixCounts.reduce((a,b)=>a+b,0);
    const mix={rate:data.p2p?.attempted?data.p2p.rate.toFixed(1):'—',aria:'实际通信路径',segs:['P2P','中继','其他'].map((label,i)=>({label,n:mixCounts[i],pct:mixTotal?mixCounts[i]/mixTotal*100:0,color:[c.s1,c.s2,c.swOff][i]}))};
    const tokenRows=this.items('tokens').filter(x=>x.state==='active'), apiKeys=this.items('apiKeys').filter(x=>x.state==='active');
    const logins=this.items('logins'), bans=this.items('bans'), keys=this.items('keys'), acl=this.items('acl');
    const settingsChange=patch=>this.change('/admin/system/settings','PATCH',patch,{close:false});
    const services=data.status?.services||[], backups=this.items('backups'), hooks=this.items('hooks'), dbData=data.db||{};
    const dd=devices.find(x=>x.id===this.g('drawerId',''));
    const dv=dd?{...ds(dd),name:dd.name,did:dd.did,icon:icon(dd),facts:Object.entries({操作系统:dd.os,所属用户:dd.user,分组:dd.group,公网地址:dd.ip,内网地址:dd.lan_ip,最后在线:this.date(dd.last_seen_at),注册时间:this.date(dd.registered_at),密钥指纹:dd.fingerprint,客户端版本:dd.client_version}).map(([label,value])=>({label,value:value||'—'})),links:maps.filter(m=>m.device_id===dd.id).map(m=>({kind:'FRP',fg:c.accent,bg:c.accentSoft,text:m.name,meta:m.public_addr})),noLinks:!maps.some(m=>m.device_id===dd.id),lvSeg:seg(['guest','member','admin'].map(l=>[l,level(l).lvLabel]),'drawerLevel',dd.level,v=>this.change(devicePath(dd),'PATCH',{level:v},{close:false})),toggle:toggleDev(dd),toggleLabel:dd.status==='active'?s.disable:s.enable,toggleIcon:I.ban,rotate:act(devicePath(dd)+'/rotate-key'),ban:()=>this.confirmAction('封禁 '+dd.name+'？',act(devicePath(dd)+'/ban')),remove:remove(devicePath(dd),'删除 '+dd.name+'？')}:{};
    const pages=[['overview',I.grid,s.pOverview,s.pOverviewSub],['devices',I.devices,s.pDevices,s.pDevicesSub],['comms',I.swap,s.pComms,s.pCommsSub],['frp',I.tunnel,s.pFrp,s.pFrpSub],['access',I.userCheck,s.pAccess,s.pAccessSub],['security',I.shield,s.pSecurity,s.pSecuritySub],['monitor',I.activity,s.pMonitor,s.pMonitorSub],['system',I.gear,s.pSystem,s.pSystemSub]];
    const current=pages.find(p=>p[0]===page), cf=this.g('cf',null), dialog=this.g('dialog',null), draft=this.g('draft',{});
    const extraActions=this.extraActions(page,canWrite,canAdmin);
    return {...common,...this.managerVals(canWrite,canAdmin),canWrite,canAdmin,writeDisabled:!canWrite||busy,adminDisabled:!canAdmin||busy,backupSchedule:settings.backup_hour===-1?'自动备份已停用':`每日 UTC ${settings.backup_hour}:00 · 保留 ${settings.backup_keep} 份`,username:me.user?.username||'—',role:me.role,serverName:settings.console_name||'Meshport',serverVersion:data.status?.version||'—',serverUptime:this.duration(data.status?.uptime_s),connectionState:this.g('stream','polling')==='live'?'实时连接':'轮询更新',apiBase:location.origin+'/api/v1',loadedAt:this.g('loadedAt','加载中'),hasLoadErrors:!!this.g('loadErrors',[]).length,loadErrors:this.g('loadErrors',[]).join('\n'),refresh:()=>this.refresh(),extraActions,alertDot:alerts.some(a=>a.status==='open')?1:0,
      themeIcon:dark?I.sun:I.moon,toggleTheme:()=>{const theme=dark?'light':'dark';localStorage.setItem('meshport.theme',theme);set({theme});},cycleLang:()=>{const next={en:'zh',zh:'ja',ja:'en'}[lang];localStorage.setItem('meshport.legacy.lang',next);set({lang:next});},
      nav:pages.map(p=>({label:p[2],icon:p[1],go:()=>this.go(p[0]),current:p[0]===page?'page':'false',bg:p[0]===page?c.accentSoft:'transparent',fg:p[0]===page?c.accent:c.text,hasBadge:false,badge:0,badgeFg:c.muted,badgeBg:c.tile})),pageTitle:current[2],pageSub:current[3],isOverview:page==='overview',isDevices:page==='devices',isComms:page==='comms',isFrp:page==='frp',isAccess:page==='access',isSecurity:page==='security',isMonitor:page==='monitor',isSystem:page==='system',showLive:['overview','comms'].includes(page),q:this.g('q',''),setQ:e=>set({q:e.target.value}),goAlerts:()=>this.go('monitor'),goSystem:()=>this.go('system'),goAccess:()=>this.go('access'),goMonitor:()=>this.go('monitor'),addDevice:()=>this.enroll(),exportAudit:()=>this.exportAudit(),checkUpdate:()=>this.checkUpdate(),stopAllFrp:()=>this.confirmAction('停止全部 Meshport 映射？',act('/admin/frp/stop-all')),openAddMap:()=>this.mapForm(),
      gc:{dev:'minmax(220px,1.6fr) 130px 140px 96px minmax(130px,1fr) 84px 104px 210px',comm:'minmax(230px,1.5fr) 96px minmax(190px,1.3fr) 100px 64px 86px 130px minmax(170px,1.2fr) 118px',frp:'minmax(120px,1fr) minmax(140px,1.1fr) 70px 130px minmax(200px,1.6fr) 110px 100px 50px 186px',acc:'minmax(170px,1.2fr) minmax(150px,1fr) 84px minmax(200px,1.6fr) 96px 120px 96px 170px',login:'96px 96px minmax(150px,1.1fr) minmax(150px,1.2fr) minmax(190px,1.4fr)',fail:'86px minmax(150px,1.4fr) minmax(130px,1.2fr) 110px'},
      kpis,bw,res:resources,resSeg:[],resFoot:[{label:'WebSocket',value:latest.ws_conns??overview.devices?.online??0},{label:s.uptime,value:this.duration(data.status?.uptime_s)}],pendTop:[...pending.map(d=>({title:d.name,meta:'设备注册待批准',icon:icon(d),...level(d.level),approve:act(devicePath(d)+'/approve'),reject:()=>this.confirmAction('拒绝此注册？',act(devicePath(d)+'/reject'))})),...approvals.map(g=>({title:g.requester+' → '+g.target,meta:g.reason,icon:I.userCheck,...level(g.level),approve:grantAction(g,'approve'),reject:grantAction(g,'reject')}))].slice(0,5),pendCount:pending.length+approvals.length,noPend:!pending.length&&!approvals.length,alertsTop,mix,
      devSeg:seg(['all','online','offline','disabled','pending','banned'].map(v=>[v,({all:s.all,online:s.online,offline:s.offline,disabled:s.disabled,pending:s.pending,banned:'封禁'})[v]+' '+(counts[v]??0)]),'devFilter','all'),devRows,devEmpty:!devRows.length,devShowing:`${devRows.length} / ${devices.length}`,
      commTiles:[{label:'活动会话',value:sessions.filter(x=>x.state!=='closed').length,unit:'',sub:'最近 24 小时'},{label:'中继消息',value:data.sessions?.relay_messages_24h?.queued??0,unit:'',sub:'等待送达'}],commSeg:seg(['all','p2p','relay','auto'].map(v=>[v,v==='all'?s.all:v]),'commFilter','all'),commRows,commEmpty:!commRows.length,
      frpsCards:nodes.map(n=>({...st(n.status==='online'?'ok':'warn',n.status),name:n.name,host:n.public_addr,ip:n.public_addr,ver:n.version||'—',ports:[n.bind_port,n.vhost_http_port,n.vhost_https_port].filter(Boolean).join(' · '),clients:n.frpc_clients,proxies:`${n.mappings_online} / ${n.mappings}`,conns:nodeStats(n).conns??0,traffic:this.bytes(nodeStats(n).rate_bps||0)+'/s'})),frpcRows:this.items('clients').map(x=>({...st('ok',s.online),name:x.device,icon:I.devices,ver:x.version,count:`${x.proxies_online??0} 映射`})),frpcCount:String(this.items('clients').length)+' frpc',frpSeg:seg([['all',s.all],['running',s.running],['stopped',s.stopped+' / '+s.stError]],'frpFilter','all'),frpTotals:`${maps.filter(m=>m.status==='online').length} / ${maps.length}`,mapRows,frpEmpty:!mapRows.length,
      accSeg:seg([['pending',s.pending],['approved',s.approved],['history',s.history],['all',s.all]],'accFilter','pending'),accRows,accEmpty:!accRows.length,levelDefs:['guest','member','admin'].map(l=>({...level(l),label:level(l).lvLabel,fg:level(l).lvFg,bg:level(l).lvBg,name:level(l).lvLabel,desc:{guest:s.lvGuestDesc,member:s.lvMemberDesc,admin:s.lvAdminDesc}[l],count:devices.filter(d=>d.level===l).length})),userRows:users.map(u=>({name:u.username,ini:u.username.slice(0,2).toUpperCase(),...level(u.role==='admin'?'admin':u.role==='operator'?'member':'guest'),lvLabel:u.role,...st(u.mfa_enabled?'ok':'warn',u.mfa_enabled?'MFA 已启用':'MFA 未启用'),email:u.email||'—',role:u.role,devices:u.devices,levels:['guest','member','admin'].map(l=>({...level(l),n:u.levels?.[l]||0}))})),
      secTiles:[{label:'异常登录',value:logins.filter(x=>x.abnormal).length,sub:'已记录',fg:c.crit,icon:I.alert},{label:'MFA',value:`${data.mfa?.enabled??0} / ${data.mfa?.console_users??0}`,sub:'控制台用户',fg:c.accent,icon:I.lock},{label:'待轮换密钥',value:keys.filter(x=>x.state!=='ok').length,sub:'设备身份密钥',fg:c.warn,icon:I.key},{label:'API 令牌',value:tokenRows.length,sub:'有效令牌',fg:c.muted,icon:I.key},{label:'已封禁设备',value:bans.length,sub:'',fg:c.muted,icon:I.ban}],loginSeg:seg([['all',s.all],['abn',s.abnormal]],'loginFilter','all'),loginRows:logins.filter(x=>this.g('loginFilter','all')==='all'||x.abnormal).map(x=>({...st(x.success?'ok':'crit',x.success?s.resOk:s.resFail),t:this.date(x.at),user:x.subject,ip:x.ip,loc:x.reason||'—',client:x.user_agent||x.method,abn:x.abnormal,abnLabel:x.abnormal_reason,rowBg:x.abnormal?c.abnRow:'transparent'})),secAlerts:alerts.filter(a=>a.category==='security').map(a=>({...alertSeverity(a),title:a.title,t:this.date(a.created_at)})),keyRows:keys.map(x=>({...st(x.state==='ok'?'ok':'warn',x.state),name:x.device,fp:x.fingerprint,age:x.age_days+' 天',rotate:act('/admin/devices/'+id(x.device_id)+'/rotate-key'),rotDisabled:String(!canWrite||x.state==='rotation_pending'),rotFg:c.text})),mfaRows:users.map(u=>({...st(u.mfa_enabled?'ok':'warn',u.mfa_enabled?'已启用':'未启用'),name:u.username,ini:u.username.slice(0,2).toUpperCase(),role:u.role,canRemind:canWrite&&!u.mfa_enabled,remind:act('/admin/security/mfa/remind','POST',{users:[u.id]})})),mfaSw:sw(settings.mfa_required,()=>this.confirmAction('更改强制 MFA 策略？',()=>this.change('/admin/security/mfa','PUT',{required:!settings.mfa_required})),s.requireMfa),tokRows:tokenRows.map(t=>({name:t.name,scope:t.scopes.join(', '),last:this.date(t.last_used_at),lastFg:c.muted,exp:this.date(t.expires_at),revoke:remove('/admin/security/tokens/'+id(t.id),'撤销令牌 '+t.name+'？')})),tokEmpty:!tokenRows.length,tokCount:String(tokenRows.length),aclSeg:seg([['allow',s.allow],['deny',s.deny]],'aclMode','allow'),aclRows:acl.filter(x=>x.action===this.g('aclMode','allow')).map(x=>({...x,icon:I.shield,fg:c.accent,remove:remove('/admin/security/acl/'+id(x.id),'删除 ACL 规则？')})),aclDraft:this.g('aclDraft',''),setAclDraft:e=>set({aclDraft:e.target.value}),addAcl:()=>this.change('/admin/security/acl','POST',{action:this.g('aclMode','allow'),cidr:this.g('aclDraft','')}).then(v=>{if(v!==undefined)set({aclDraft:''});}),banRows:bans.map(b=>({...b,name:b.name||b.device,reason:b.ban_reason||b.reason,unban:act('/admin/devices/'+id(b.id||b.device_id)+'/unban')})),banEmpty:!bans.length,banCount:String(bans.length),
      rangeSeg:seg(['1h','24h','7d'].map(x=>[x,x]),'range','24h',()=>this.refresh('monitor')),cpu,mem,net,rate,relay,frpBars,failRows:this.items('failures').map(x=>({...st('warn',x.kind),t:this.date(x.at),parties:(x.a||x.a_id)+' ↔ '+(x.b||x.b_id),reason:x.reason})),failCount:String(this.items('failures').length),logSeg:seg(['all','INFO','WARN','ERROR'].map(x=>[x,x==='all'?s.all:x]),'logLv','all'),logRows:(this.g('pausedLogs',null)||this.items('logs')).filter(x=>this.g('logLv','all')==='all'||x.level.toUpperCase()===this.g('logLv','all')).slice(0,50).map(x=>({t:this.date(x.ts),lv:x.level,src:x.source,msg:x.msg,fg:x.level.toLowerCase()==='error'?'#FF8A7A':x.level.toLowerCase()==='warn'?'#F6B556':'#7FD3BC'})),pausedStr:String(!!this.g('pausedLogs',null)),pauseIcon:this.g('pausedLogs',null)?I.play:I.pause,pauseLabel:this.g('pausedLogs',null)?s.resume:s.pause,togglePause:()=>set({pausedLogs:this.g('pausedLogs',null)?null:this.items('logs')}),alertHist:alerts.map(a=>({...alertSeverity(a),...st(a.status==='resolved'?'ok':'warn',a.status),title:a.title,t:this.date(a.created_at),dur:this.date(a.resolved_at)})),
      svcRows:services.map(v=>({...st(v.status==='running'?'ok':v.status==='degraded'?'warn':'off',v.status),...v,role:v.kind,up:this.duration(data.status?.uptime_s),btn:v.restartable?'重启':'无需重启',busy:String(!canAdmin||!v.restartable||busy),restart:()=>{if(v.restartable)this.confirmAction('重启 '+v.name+'？',act('/admin/system/services/'+id(v.name)+'/restart'));}})),svcSummary:`${services.filter(x=>x.status==='running').length} / ${services.length}`,db:{...st(dbData.integrity==='ok'?'ok':'off',dbData.integrity||'—'),engine:dbData.engine||'—',size:this.bytes(dbData.size_bytes),status:dbData.integrity||'—',name:dbData.engine||'—',ver:dbData.schema_version,used:this.bytes(dbData.size_bytes),max:'—',pct:0},dbMeters:[],dbFacts:[{label:'数据库',value:dbData.path||'—'},{label:'WAL',value:this.bytes(dbData.wal_bytes)},{label:'完整性',value:dbData.integrity||'—'},...Object.entries(dbData.rows||{}).map(([label,value])=>({label,value}))],verRows:this.items('versions').map(v=>({...v,cur:v.version,lat:'—',fg:c.muted,bg:c.tile,label:'已安装'})),bkRows:backups.map(b=>({...st(b.status==='ok'?'ok':b.status==='failed'?'crit':'warn',b.status),t:this.date(b.created_at),size:this.bytes(b.size),kind:b.kind,dl:()=>{if(b.status==='ok')this.download('/admin/system/backups/'+id(b.id)+'/download');}})),backupNow:act('/admin/system/backups'),backupBusy:String(busy),backupBtn:busy?'备份中…':s.backupNow,hookRows:hooks.map(h=>({...h,ev:h.events.join(', '),last:h.last_error||String(h.last_status||'尚未投递'),lastFg:h.last_error?c.crit:c.muted,status:h.last_error||String(h.last_status||'尚未投递'),checked:String(h.enabled),label:h.name,toggle:act('/admin/system/webhooks/'+id(h.id),'PATCH',{enabled:!h.enabled}),swBg:h.enabled?c.accentBg:c.swOff,knobX:h.enabled?18:0})),addHook:()=>this.hookForm(),keyList:apiKeys.map(k=>({name:k.name,key:k.prefix+'…',created:this.date(k.created_at),scope:k.scopes.join(','),copy:()=>this.copy(k.prefix),revoke:remove('/admin/system/api-keys/'+id(k.id),'撤销 API 密钥？')})),createKey:()=>this.tokenForm('key'),endpoints:this.items('endpoints').filter(x=>x.path.includes('/admin/')).slice(0,12).map(e=>({m:e.method,p:e.path,d:e.summary,fg:c.accent,bg:c.accentSoft})),openDocs:()=>window.open('/api/docs','_blank','noopener'),updTitle:data.update?.available?'有可用更新':data.update?.configured===false?'未配置更新源':'更新检查',updSub:data.update?.error||data.update?.notes||data.update?.latest||'当前版本 '+(data.status?.version||'—'),updBtn:'安装已签名更新',updDisabled:String(!canAdmin||busy||!data.update?.available),installUpdate:()=>this.confirmAction('安装更新并重启服务？',act('/admin/system/update/install')),chanSeg:seg([['stable',s.stable],['beta',s.beta]],'chan',settings.update_channel||'stable',v=>settingsChange({update_channel:v})),autoSw:sw(settings.auto_update,()=>settingsChange({auto_update:!settings.auto_update}),s.autoUpdate),retSeg:seg([7,30,90].map(x=>[String(x),x+' 天']),'ret',String(settings.log_retention_days||14),v=>settingsChange({log_retention_days:Number(v)})),regSw:sw(settings.open_registration,()=>settingsChange({open_registration:!settings.open_registration}),s.openReg),swatches:Object.entries(this.accents()).map(([key,a])=>({label:key,color:a.bg,pressed:String(key===this.g('accent','jade')),ring:key===this.g('accent','jade')?a.bg:'transparent',check:key===this.g('accent','jade')?I.check:'',pick:()=>{set({accent:key});if(canAdmin)settingsChange({accent:key});}})),accentName:this.g('accent','jade'),
      hasDrawer:this.g('overlay','')==='drawer'&&!!dd,dv,hasMapModal:false,mf:{},closeOverlay:()=>set({overlay:null}),hasConfirm:!!cf,cf:cf||{},cancelConfirm:()=>{this._cfn=null;set({cf:null});},doConfirm:()=>{const fn=this._cfn;this._cfn=null;set({cf:null});fn?.();},hasDialog:!!dialog,dialogTitle:dialog?.title||'',dialogError:this.g('dialogError',''),dialogFields:(dialog?.fields||[]).map(f=>({...f,isSelect:f.type==='select',isCheckbox:f.type==='checkbox',isInput:!['select','checkbox'].includes(f.type),inputType:f.type==='list'?'text':f.type,value:draft[f.key]??'',checked:!!draft[f.key],options:(f.options||[]).map(([value,label])=>({value,label})),change:e=>set({draft:{...this.g('draft',{}),[f.key]:f.type==='checkbox'?e.target.checked:e.target.value}})})),submitForm:e=>this.submitForm(e),closeDialog:()=>{if(!busy)set({dialog:null,draft:{}});},account:()=>this.account(),mfaSetup:()=>this.mfaSetup(),mfaEnable:()=>this.mfaConfirm(),mfaDisable:()=>this.mfaConfirm(true),ownMfa:!!me.user?.mfa_enabled
    };
  }
  mapForm(m) {
    const nodes=this.items('nodes'), devices=this.items('devices').filter(d=>['member','admin'].includes(d.level)&&d.status==='active');
    if(!nodes.length||!devices.length){this.flash('请先创建 frps 节点，并注册具有 member 或 admin 等级的装置。');return;}
    const fields=[{key:'name',label:'映射名称',required:true},{key:'device',label:'frpc 装置',type:'select',options:devices.map(x=>[x.id,x.name]),required:true},{key:'node',label:'frps 节点',type:'select',options:nodes.map(x=>[x.id,x.name]),required:true},{key:'proto',label:'协议',type:'select',options:['tcp','udp','http','https','stcp','xtcp'].map(x=>[x,x.toUpperCase()])},{key:'local_ip',label:'本机服务地址'},{key:'local_port',label:'本机服务端口',type:'number',required:true},{key:'remote_port',label:'TCP/UDP 公网端口（其他协议填 0）',type:'number'},{key:'domains',label:'HTTP/HTTPS 域名（逗号分隔）',type:'list'},{key:'subdomain',label:'子域名前缀（可选）'},{key:'secret_key',label:'STCP/XTCP 密钥',type:'password',omitEmpty:true},{key:'use_encryption',label:'加密代理流量',type:'checkbox'},{key:'use_compression',label:'压缩',type:'checkbox'},{key:'bandwidth_limit',label:'带宽上限（例如 512KB，留空不限）'},{key:'enabled',label:'启用',type:'checkbox'}];
    this.form(m?'编辑映射':'新增映射',fields,d=>{
      if(m)delete d.device;
      return this.change('/admin/frp/mappings'+(m?'/'+encodeURIComponent(m.id):''),m?'PATCH':'POST',d);
    },m?{...m,device:m.device_id,node:m.node_id,domains:(m.domains||[]).join(',')}:{device:devices[0].id,node:nodes[0].id,proto:'tcp',local_ip:'127.0.0.1',local_port:22,remote_port:20022,domains:'',enabled:true,use_encryption:true});
  }
  async checkUpdate() { try {const v=await this.request('/admin/system/update');this._data.update=v;this.setState({data:{...this._data}});this.showResult('更新检查',v);}catch(e){this.setState({error:e.message});} }
  chart(key,sets,c,percent=false) {
    const valid=sets.map(([name,values,color])=>({name,values:(values||[]).filter(x=>Number.isFinite(Number(x.v))),color}));
    const max=percent?100:Math.max(1,...valid.flatMap(x=>x.values.map(p=>Number(p.v))));
    const primary=valid[0]?.values||[], hover=this.g('hov',null), index=hover?.k===key?hover.i:-1;
    const point=(p,i,n)=>`${n>1?i/(n-1)*600:0} ${100-Number(p.v)/max*100}`;
    return {grid:[1,.5,0].map(f=>({top:(1-f)*100,label:percent?Math.round(f*max)+'%':this.bytes(f*max)+'/s'})),lines:valid.map(x=>({color:x.color,d:x.values.map((p,i)=>(i?'L':'M')+point(p,i,x.values.length)).join(' ')})),areas:[],ends:[],hov:index>=0&&!!primary[index],hx:primary.length>1?index/(primary.length-1)*100:0,tipX:primary.length>1?index/(primary.length-1)*100:0,tipShift:'12px',dots:[],tipTitle:this.date(primary[index]?.t),tipRows:valid.filter(x=>x.values[index]).map(x=>({label:x.name,color:x.color,value:percent?Number(x.values[index].v).toFixed(1)+'%':this.bytes(x.values[index].v)+'/s'})),cols:primary.map((_,i)=>({enter:()=>this.setState({hov:{k:key,i}})})),leave:()=>this.setState({hov:null}),xl:primary.length?[{t:this.date(primary[0].t)},{t:this.date(primary.at(-1).t)}]:[{t:'暂无采样数据'}],legend:valid.map(x=>({label:x.name,color:x.color,value:x.values.length?(percent?Number(x.values.at(-1).v).toFixed(1)+'%':this.bytes(x.values.at(-1).v)+'/s'):'—'})),now:primary.length?(percent?Number(primary.at(-1).v).toFixed(1)+'%':this.bytes(primary.at(-1).v)+'/s'):'—'};
  }
  extraActions(page,canWrite,canAdmin) {
    const action=(label,run,min='write')=>({label,run,disabled:this.g('busy',false)||(min==='admin'?!canAdmin:min==='write'?!canWrite:false)});
    const manager=(title,resource)=>()=>this.manage(title,resource);
    const actions={
      overview:[],devices:[action('待审批 / 注册码',manager('设备注册与审批','enroll')),action('分组管理',manager('设备分组','groups')),action('编辑设备',manager('设备管理','devices'))],
      comms:[],frp:[action('节点管理',manager('frps 节点','nodes'),'admin')],
      access:[action('直接授予等级',()=>this.grantForm()),action('用户管理',manager('用户','users'),'admin')],
      security:[action('创建个人令牌',()=>this.tokenForm(),'any'),action('我的 MFA',()=>this.g('me',{}).user?.mfa_enabled?this.mfaConfirm(true):this.mfaSetup(),'any'),action('确认启用 MFA',()=>this.mfaConfirm(),'any')],
      monitor:[action('告警处理',manager('告警','alerts')),action('查看审计',()=>this.viewAudit(),'any')],
      system:[action('系统设置',()=>this.settingsForm(),'admin'),action('Webhook 管理',manager('Webhook','hooks'),'admin'),action('备份管理',manager('备份','backups'),'admin')]
    };
    return actions[page]||[];
  }
  manage(title,resource) { this.setState({manager:{title,resource}}); }
  managerVals(canWrite,canAdmin) {
    const manager=this.g('manager',null), resource=manager?.resource;
    const id=x=>encodeURIComponent(x), change=(path,method='POST',body={})=>()=>this.change(path,method,body,{close:false});
    const del=(path,title)=>()=>this.confirmAction(title,change(path,'DELETE'));
    const action=(label,run,admin=false)=>({label,run,disabled:this.g('busy',false)||!(admin?canAdmin:canWrite)});
    const openConfig=n=>async()=>{try{this.showResult('frps 配置（包含凭据）',await this.request('/admin/frp/nodes/'+id(n.id)+'/config'));}catch(e){this.setState({error:e.message});}};
    let records=[];
    const creators={enroll:()=>this.enroll(),groups:()=>this.groupForm(),users:()=>this.userForm(),nodes:()=>this.nodeForm(),hooks:()=>this.hookForm()};
    if(resource==='enroll') records=[...this.items('devices').filter(d=>d.status==='pending').map(d=>({title:d.name,detail:`注册待审批 · ${d.did}`,actions:[action('批准',change('/admin/devices/'+id(d.id)+'/approve')),action('拒绝',()=>this.confirmAction('拒绝注册 '+d.name+'？',change('/admin/devices/'+id(d.id)+'/reject')))]})),...this.items('enroll').map(t=>({title:t.note||t.id,detail:`${t.level} · 剩余 ${t.uses_left} 次 · 到期 ${this.date(t.expires_at)}`,actions:[action('撤销注册码',del('/admin/enroll-tokens/'+id(t.id),'撤销此注册码？'))]}))];
    if(resource==='groups') records=this.items('groups').map(g=>({title:g.name,detail:`${g.description||''} · ${g.devices??0} 个设备`,actions:[action('编辑',()=>this.groupForm(g)),action('删除',del('/admin/groups/'+id(g.id),'删除分组 '+g.name+'？'))]}));
    if(resource==='devices') records=this.items('devices').map(d=>({title:d.name,detail:`${d.status} · ${d.level} · ${d.did}`,actions:[action('编辑',()=>this.deviceEdit(d)),action('远程指令',()=>this.commandForm(d)),action('删除',del('/admin/devices/'+id(d.id),'删除装置 '+d.name+'？'))]}));
    if(resource==='users') records=this.items('users').map(u=>({title:u.username,detail:`${u.role} · ${u.disabled?'已停用':'可用'} · MFA ${u.mfa_enabled?'已启用':'未启用'}`,actions:[action('编辑',()=>this.userForm(u),true),action('重置 MFA',()=>this.confirmAction('重置 '+u.username+' 的 MFA？',change('/admin/users/'+id(u.id)+'/reset-mfa')),true),action('删除',del('/admin/users/'+id(u.id),'删除用户 '+u.username+'？'),true)]}));
    if(resource==='nodes') records=this.items('nodes').map(n=>({title:n.name,detail:`${n.public_addr}:${n.bind_port} · ${n.status} · ${n.last_error||''}`,actions:[action('编辑',()=>this.nodeForm(n),true),action(n.enabled?'停用':'启用',()=>this.confirmAction((n.enabled?'停用':'启用')+'节点 '+n.name+'？',change('/admin/frp/nodes/'+id(n.id),'PATCH',{enabled:!n.enabled})),true),...(n.managed?[action('重启',()=>this.confirmAction('重启节点 '+n.name+'？',change('/admin/frp/nodes/'+id(n.id)+'/restart')))]:[]),action('配置',openConfig(n),true),action('删除',del('/admin/frp/nodes/'+id(n.id),'删除节点及其映射？'),true)]}));
    if(resource==='hooks') records=this.items('hooks').map(h=>({title:h.name||h.url,detail:`${h.url} · ${h.enabled?'已启用':'已停用'} · ${h.last_error||h.last_status||'尚未投递'}`,actions:[action('编辑',()=>this.hookForm(h),true),action('发送测试事件',change('/admin/system/webhooks/'+id(h.id)+'/test'),true),action('删除',del('/admin/system/webhooks/'+id(h.id),'删除 Webhook？'),true)]}));
    if(resource==='backups') records=this.items('backups').map(b=>({title:b.file,detail:`${b.status} · ${this.bytes(b.size)} · ${this.date(b.created_at)}`,actions:[{label:'下载',run:()=>this.download('/admin/system/backups/'+id(b.id)+'/download'),disabled:!canAdmin||b.status!=='ok'},action('删除',del('/admin/system/backups/'+id(b.id),'删除此备份？'),true)]}));
    if(resource==='alerts') records=this.items('alerts').map(a=>({title:a.title,detail:`${a.severity} · ${a.status} · ${this.date(a.created_at)} · ${a.detail||''}`,actions:a.status==='resolved'?[]:[action('确认',change('/admin/alerts/'+id(a.id)+'/ack')),action('标记解决',change('/admin/alerts/'+id(a.id)+'/resolve'))]}));
    return {hasManager:!!manager,managerTitle:manager?.title||'',managerRows:records,managerEmpty:!records.length,closeManager:()=>this.setState({manager:null}),managerCanCreate:!!creators[resource]&&(resource==='users'||resource==='nodes'||resource==='hooks'?canAdmin:canWrite),managerCreate:creators[resource]||(()=>{})};
  }
  commandForm(d) {
    this.form('远程指令 · '+d.name,[{key:'name',label:'指令',type:'select',options:['ping','reload-frpc','rotate-key','refresh-peers','collect-logs','disconnect','restart-client'].map(x=>[x,x])},{key:'timeout_s',label:'等待秒数',type:'number'}],v=>this.change('/admin/devices/'+encodeURIComponent(d.id)+'/command','POST',v,{result:'指令执行结果'}),{name:'ping',timeout_s:10});
  }
  async viewAudit() { try { const d=await this.request('/admin/audit?limit=200');this.showResult('最近 200 条审计记录',d); }catch(e){this.setState({error:e.message});} }
}
