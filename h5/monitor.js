class UDPlexMonitor {
    constructor() {
        this.components = new Map();
        this.detailedData = new Map();
        this.autoRefresh = true;
        this.refreshInterval = 5000;
        this.refreshTimer = null;
        this.baseUrl = '';
        this.componentsLoaded = false;

        this.initEventListeners();
        this.startMonitoring();
    }

    initEventListeners() {
        document.getElementById('autoRefreshBtn').addEventListener('click', () => {
            this.toggleAutoRefresh();
        });

        document.getElementById('refreshBtn').addEventListener('click', () => {
            this.refreshData();
        });

        document.getElementById('clearHighlightBtn').addEventListener('click', () => {
            this.clearHighlights();
        });

        document.getElementById('intervalSelect').addEventListener('change', (event) => {
            this.refreshInterval = parseInt(event.target.value, 10);
            document.getElementById('refreshInterval').textContent = `${this.refreshInterval / 1000} 秒`;
            if (this.autoRefresh) {
                this.startRefreshTimer();
            }
        });
    }

    toggleAutoRefresh() {
        this.autoRefresh = !this.autoRefresh;
        const button = document.getElementById('autoRefreshBtn');
        button.textContent = this.autoRefresh ? '自动刷新' : '手动刷新';
        button.classList.toggle('active', this.autoRefresh);

        if (this.autoRefresh) {
            this.startRefreshTimer();
        } else {
            this.stopRefreshTimer();
        }
    }

    startRefreshTimer() {
        this.stopRefreshTimer();
        this.refreshTimer = setInterval(() => {
            this.refreshData();
        }, this.refreshInterval);
    }

    stopRefreshTimer() {
        if (this.refreshTimer) {
            clearInterval(this.refreshTimer);
            this.refreshTimer = null;
        }
    }

    async startMonitoring() {
        await this.loadComponentsOnce();
        if (this.autoRefresh) {
            this.startRefreshTimer();
        }
    }

    async loadComponentsOnce() {
        try {
            document.getElementById('connectionStatus').textContent = '加载组件中...';
            const response = await fetch(`${this.baseUrl}/api/components`);
            if (!response.ok) {
                throw new Error(`HTTP ${response.status}: ${response.statusText}`);
            }

            const components = await response.json();
            this.components.clear();
            this.detailedData.clear();

            for (const component of components) {
                this.components.set(component.tag, component);
            }

            await this.refreshAllComponentDetails();

            this.componentsLoaded = true;
            this.renderComponents();
            this.updateStatus('在线', true);
            document.getElementById('lastUpdate').textContent = new Date().toLocaleTimeString();
        } catch (error) {
            console.error('Error fetching data:', error);
            this.showError(`连接失败: ${error.message}`);
            this.updateStatus('离线', false);
        }
    }

    async refreshData() {
        if (!this.componentsLoaded) {
            await this.loadComponentsOnce();
            return;
        }

        try {
            document.getElementById('connectionStatus').textContent = '刷新中...';
            const highlightedTags = Array.from(document.querySelectorAll('.component-card.highlight'))
                .map((card) => card.dataset.tag);

            await this.refreshAllComponentDetails();
            this.renderComponents();

            highlightedTags.forEach((tag) => {
                const card = document.querySelector(`[data-tag="${tag}"]`);
                if (card) {
                    card.classList.add('highlight');
                }
            });

            this.updateStatus('在线', true);
            document.getElementById('lastUpdate').textContent = new Date().toLocaleTimeString();
        } catch (error) {
            console.error('Error refreshing data:', error);
            this.showError(`刷新失败: ${error.message}`);
            this.updateStatus('离线', false);
        }
    }

    async refreshAllComponentDetails() {
        const tasks = [];
        for (const component of this.components.values()) {
            tasks.push(this.fetchComponentDetails(component));
        }
        await Promise.all(tasks);
    }

    getDetailEndpoint(component) {
        switch (component.type) {
            case 'listen':
                return `/api/listen/${component.tag}`;
            case 'forward':
                return `/api/forward/${component.tag}`;
            case 'tcp_tunnel_listen':
                return `/api/tcp_tunnel_listen/${component.tag}`;
            case 'tcp_tunnel_forward':
                return `/api/tcp_tunnel_forward/${component.tag}`;
            case 'load_balancer':
                return `/api/load_balancer/${component.tag}`;
            case 'filter':
                return `/api/filter/${component.tag}`;
            case 'ip_router':
                return `/api/ip_router/${component.tag}`;
            case 'wg':
                return `/api/wg/${component.tag}`;
            default:
                return '';
        }
    }

    async fetchComponentDetails(component) {
        const endpoint = this.getDetailEndpoint(component);
        if (!endpoint) {
            return;
        }

        try {
            const response = await fetch(`${this.baseUrl}${endpoint}`);
            if (!response.ok) {
                throw new Error(`HTTP ${response.status}: ${response.statusText}`);
            }

            const data = await response.json();
            this.detailedData.set(component.tag, data);
        } catch (error) {
            console.warn(`Failed to fetch details for ${component.tag}:`, error);
        }
    }

    renderComponents() {
        const container = document.getElementById('componentsContainer');
        container.innerHTML = '';

        for (const component of this.components.values()) {
            container.appendChild(this.createComponentCard(component));
        }

        document.getElementById('loadingContainer').style.display = 'none';
        container.style.display = 'grid';
        this.attachGeoButtons();
        this.attachDetourClicks();
    }

    createComponentCard(component) {
        const card = document.createElement('div');
        card.className = 'component-card';
        card.dataset.tag = component.tag;

        card.addEventListener('click', () => {
            this.highlightDetourTargets(component);
        });

        const detailedData = this.detailedData.get(component.tag);
        card.innerHTML = `
            <div class="component-header">
                <div class="component-tag">${this.escapeHtml(component.tag)}</div>
                <div class="component-type ${component.type}">${this.escapeHtml(component.type)}</div>
            </div>

            ${this.renderComponentDetails(component, detailedData)}
            ${this.renderDetourSection(component)}
            ${this.renderIPRouterActions(component, detailedData)}
            ${this.renderConnectionsInfo(component, detailedData)}
        `;

        return card;
    }

    renderComponentDetails(component, detailedData) {
        if (component.type === 'ip_router' && detailedData) {
            return this.renderIPRouterDetails(component, detailedData);
        }

        if (component.type === 'wg') {
            return this.renderWireGuardDetails(component, detailedData);
        }

        const details = [];

        if (component.listen_addr) {
            details.push(['监听地址', component.listen_addr]);
        }
        if (Array.isArray(component.forwarders) && component.forwarders.length > 0) {
            details.push(['转发目标', component.forwarders.join(', ')]);
        }
        if (component.interface_name) {
            details.push(['默认网卡', component.interface_name]);
        }
        if (component.timeout) {
            details.push(['超时时间', `${component.timeout}s`]);
        }
        if (component.window_size) {
            details.push(['窗口大小', component.window_size]);
        }
        if (Array.isArray(component.use_proto_detectors) && component.use_proto_detectors.length > 0) {
            details.push(['协议探测器', component.use_proto_detectors.join(', ')]);
        }
        if (detailedData && detailedData.count !== undefined) {
            details.push(['连接数', detailedData.count]);
        }
        if (detailedData && detailedData.total_connections !== undefined) {
            details.push(['总连接数', detailedData.total_connections]);
        }
        if (detailedData && detailedData.bits_per_sec !== undefined) {
            const rate = component.type === 'load_balancer'
                ? this.formatRate(detailedData.bits_per_sec)
                : `${(detailedData.bits_per_sec / 1024).toFixed(2)} Kbps`;
            details.push(['流量速率', rate]);
        }
        if (detailedData && detailedData.packets_per_sec !== undefined) {
            details.push(['包速率', `${detailedData.packets_per_sec} pps`]);
        }
        if (detailedData && detailedData.average_delay_ms !== undefined && detailedData.average_delay_ms > 0) {
            details.push(['平均延迟', `${detailedData.average_delay_ms.toFixed(2)} ms`]);
        }

        if (details.length === 0) {
            return '';
        }

        return this.renderDetailList(details);
    }

    renderIPRouterDetails(component, detailedData) {
        const rules = Array.isArray(detailedData.rules) ? detailedData.rules : [];
        const miss = Array.isArray(detailedData.detour_miss)
            ? detailedData.detour_miss
            : (Array.isArray(component.detour_miss) ? component.detour_miss : []);
        const geo = detailedData.geoip || {};

        const details = [];
        if (rules.length > 0) {
            const ruleText = rules
                .map((rule) => `${rule.match} -> [${(rule.targets || []).join(', ')}]`)
                .join('<br>');
            details.push(['规则', ruleText, true]);
        }
        if (miss.length > 0) {
            details.push(['未命中', `[${miss.join(', ')}]`]);
        }
        if (typeof geo.db_loaded === 'boolean') {
            details.push(['GeoIP 已加载', geo.db_loaded ? '是' : '否']);
        }
        if (geo.geoip_url) {
            details.push(['GeoIP URL', geo.geoip_url]);
        }
        if (geo.geoip_path) {
            details.push(['GeoIP 本地路径', geo.geoip_path]);
        }
        if (geo.update_interval_sec) {
            details.push(['更新间隔', `${geo.update_interval_sec}s`]);
        }

        return this.renderDetailList(details);
    }

    renderWireGuardDetails(component, detailedData) {
        const details = [];
        const peerCount = detailedData?.peer_count ?? component.peer_count;
        const interfaceName = detailedData?.interface_name ?? component.interface_name;
        const actualInterfaceName = detailedData?.actual_interface_name;
        const effectiveRoutes = Array.isArray(detailedData?.effective_routes) ? detailedData.effective_routes : [];
        const routes = Array.isArray(detailedData?.routes)
            ? detailedData.routes
            : (Array.isArray(component.routes) ? component.routes : []);
        const addresses = Array.isArray(detailedData?.addresses)
            ? detailedData.addresses
            : (Array.isArray(component.addresses) ? component.addresses : []);

        if (interfaceName) {
            details.push(['接口名', interfaceName]);
        }
        if (actualInterfaceName && actualInterfaceName !== interfaceName) {
            details.push(['实际接口名', actualInterfaceName]);
        }
        if (typeof detailedData?.is_running === 'boolean') {
            details.push(['运行状态', detailedData.is_running ? '运行中' : '未运行']);
        }
        if (detailedData?.listen_port || component.listen_port) {
            details.push(['监听端口', detailedData?.listen_port ?? component.listen_port]);
        }
        if (detailedData?.mtu) {
            details.push(['MTU', detailedData.mtu]);
        }
        if (Array.isArray(addresses) && addresses.length > 0) {
            details.push(['地址', addresses.join('<br>'), true]);
        }
        if (Array.isArray(routes) && routes.length > 0) {
            details.push(['静态路由', routes.join('<br>'), true]);
        }
        if (effectiveRoutes.length > 0) {
            details.push(['生效路由', effectiveRoutes.join('<br>'), true]);
        }
        if (typeof detailedData?.route_allowed_ips === 'boolean') {
            details.push(['自动写入 AllowedIPs', detailedData.route_allowed_ips ? '是' : '否']);
        }
        if (typeof detailedData?.setup_interface === 'boolean') {
            details.push(['自动配置接口', detailedData.setup_interface ? '是' : '否']);
        }
        if (typeof detailedData?.reuse_incoming_detour === 'boolean') {
            details.push(['回包复用入站链路', detailedData.reuse_incoming_detour ? '是' : '否']);
        }
        if (typeof peerCount === 'number') {
            details.push(['Peer 数量', peerCount]);
        }
        if (typeof detailedData?.send_timeout_ms === 'number') {
            details.push(['发送超时', `${detailedData.send_timeout_ms} ms`]);
        }
        if (typeof detailedData?.rx_queue_length === 'number' && typeof detailedData?.rx_queue_capacity === 'number') {
            details.push(['接收队列', `${detailedData.rx_queue_length} / ${detailedData.rx_queue_capacity}`]);
        }
        if (detailedData?.runtime_error) {
            details.push(['运行态错误', detailedData.runtime_error]);
        }

        let html = this.renderDetailList(details);
        if (Array.isArray(detailedData?.peers) && detailedData.peers.length > 0) {
            html += this.renderWireGuardPeers(detailedData.peers);
        }
        return html;
    }

    renderWireGuardPeers(peers) {
        const items = peers.map((peer, index) => {
            const badges = [];
            if (peer.runtime_present) {
                badges.push('<span class="peer-badge success">运行态已识别</span>');
            } else {
                badges.push('<span class="peer-badge warn">仅配置态</span>');
            }
            if (peer.runtime_only) {
                badges.push('<span class="peer-badge">仅运行态</span>');
            }
            if (peer.has_preshared_key) {
                badges.push('<span class="peer-badge">PSK</span>');
            }

            const rows = [];
            rows.push(['Public Key', this.escapeHtml(peer.public_key || `peer-${index + 1}`)]);
            if (peer.endpoint) {
                rows.push(['配置 Endpoint', this.escapeHtml(peer.endpoint)]);
            }
            if (peer.runtime_endpoint) {
                rows.push(['运行 Endpoint', this.escapeHtml(peer.runtime_endpoint)]);
            }
            if (Array.isArray(peer.allowed_ips) && peer.allowed_ips.length > 0) {
                rows.push(['配置 AllowedIPs', this.escapeHtml(peer.allowed_ips.join(', '))]);
            }
            if (Array.isArray(peer.runtime_allowed_ips) && peer.runtime_allowed_ips.length > 0) {
                rows.push(['运行 AllowedIPs', this.escapeHtml(peer.runtime_allowed_ips.join(', '))]);
            }
            if (peer.persistent_keepalive) {
                rows.push(['配置 Keepalive', `${peer.persistent_keepalive}s`]);
            }
            if (peer.runtime_persistent_keepalive) {
                rows.push(['运行 Keepalive', `${peer.runtime_persistent_keepalive}s`]);
            }
            if (peer.last_handshake_time) {
                rows.push(['最近握手', this.formatDateTime(peer.last_handshake_time)]);
            }
            if (typeof peer.rx_bytes === 'number') {
                rows.push(['接收字节', this.formatBytes(peer.rx_bytes)]);
            }
            if (typeof peer.tx_bytes === 'number') {
                rows.push(['发送字节', this.formatBytes(peer.tx_bytes)]);
            }
            if (peer.protocol_version) {
                rows.push(['协议版本', String(peer.protocol_version)]);
            }

            return `
                <div class="peer-card">
                    <div class="peer-header">
                        <div class="peer-title">Peer ${index + 1}</div>
                        <div class="peer-badges">${badges.join('')}</div>
                    </div>
                    <div class="peer-grid">
                        ${rows.map(([label, value]) => `
                            <div class="peer-row">
                                <span class="peer-label">${label}</span>
                                <span class="peer-value">${value}</span>
                            </div>
                        `).join('')}
                    </div>
                </div>
            `;
        }).join('');

        return `
            <div class="connections-info">
                <div class="detour-title">WireGuard Peers</div>
                <div class="peer-list">
                    ${items}
                </div>
            </div>
        `;
    }

    renderDetourSection(component) {
        if (!component.detour) {
            return '';
        }

        let html = '<div class="detour-section"><div class="detour-title">路由目标 (Detour)</div>';

        if (Array.isArray(component.detour)) {
            if (component.detour.length > 0 && typeof component.detour[0] === 'object') {
                component.detour.forEach((rule) => {
                    html += `
                        <div class="detour-protocol">
                            <span class="protocol-label">规则: ${this.escapeHtml(rule.rule)}</span>
                            ${(rule.targets || []).map((target) => this.renderDetourTag(target)).join('')}
                        </div>
                    `;
                });
            } else {
                html += component.detour.map((target) => this.renderDetourTag(target)).join('');
            }
        } else if (typeof component.detour === 'object') {
            Object.entries(component.detour).forEach(([protocol, targets]) => {
                html += `
                    <div class="detour-protocol">
                        <span class="protocol-label">${this.escapeHtml(protocol)}:</span>
                        ${(targets || []).map((target) => this.renderDetourTag(target)).join('')}
                    </div>
                `;
            });
        }

        const detail = this.detailedData.get(component.tag) || {};
        const missArray = Array.isArray(detail.detour_miss)
            ? detail.detour_miss
            : (Array.isArray(component.detour_miss) ? component.detour_miss : []);
        if (missArray.length > 0) {
            html += `
                <div class="detour-protocol">
                    <span class="protocol-label">未命中:</span>
                    ${missArray.map((target) => this.renderDetourTag(target)).join('')}
                </div>
            `;
        }

        html += '</div>';
        return html;
    }

    renderIPRouterActions(component, detailedData) {
        if (component.type !== 'ip_router') {
            return '';
        }

        const geo = detailedData?.geoip || {};
        const disabled = geo.geoip_url ? '' : 'disabled';
        return `
            <div class="detour-section">
                <div class="detour-title">IP Router 操作</div>
                <button class="btn geo-update-btn" data-tag="${this.escapeHtml(component.tag)}" ${disabled}>更新 GeoIP 数据库</button>
            </div>
        `;
    }

    renderConnectionsInfo(component, detailedData) {
        if (!detailedData || component.type === 'wg') {
            return '';
        }

        if (!Array.isArray(detailedData.connections) && !Array.isArray(detailedData.pools)) {
            return '';
        }

        let html = '<div class="connections-info"><div class="detour-title">连接信息</div>';

        if (Array.isArray(detailedData.connections)) {
            detailedData.connections.forEach((connection) => {
                html += this.renderConnectionRow(connection, false);
            });
        }

        if (Array.isArray(detailedData.pools)) {
            detailedData.pools.forEach((pool) => {
                html += `
                    <div class="connection-item">
                        <div class="connection-main">
                            <strong>池 ${this.escapeHtml(pool.pool_id || '-')}</strong>
                            <span>${pool.conn_count || 0} 个连接</span>
                            ${pool.target_count ? `<span>目标数: ${pool.target_count}</span>` : ''}
                            ${pool.target_spec ? `<span>目标: ${this.escapeHtml(pool.target_spec)}</span>` : ''}
                            ${pool.interface_name ? `<span>网卡: ${this.escapeHtml(pool.interface_name)}</span>` : ''}
                        </div>
                    </div>
                `;
                if (Array.isArray(pool.connections)) {
                    pool.connections.forEach((connection) => {
                        html += this.renderConnectionRow(connection, true);
                    });
                }
            });
        }

        html += '</div>';
        return html;
    }

    renderConnectionRow(connection, indented) {
        const status = connection.is_connected !== undefined
            ? (connection.is_connected ? 'online' : 'offline')
            : (connection.is_authenticated ? 'online' : 'warning');
        const address = connection.address || connection.remote_addr || '-';
        const lastActive = connection.last_active
            ? `最后活跃: ${this.formatDateTime(connection.last_active)}`
            : '';
        const extraParts = [];
        if (connection.target_spec) {
            extraParts.push(`目标: ${this.escapeHtml(connection.target_spec)}`);
        }
        if (connection.interface_name) {
            extraParts.push(`网卡: ${this.escapeHtml(connection.interface_name)}`);
        }
        if (typeof connection.heartbeat_loss_rate === 'number') {
            extraParts.push(`丢包率: ${connection.heartbeat_loss_rate.toFixed(2)}%`);
        }

        return `
            <div class="connection-item ${indented ? 'connection-item-nested' : ''}">
                <div class="connection-main">
                    <span><span class="status-indicator status-${status}"></span>${this.escapeHtml(address)}</span>
                    ${lastActive ? `<span>${this.escapeHtml(lastActive)}</span>` : ''}
                </div>
                ${extraParts.length > 0 ? `<div class="connection-meta">${extraParts.join(' | ')}</div>` : ''}
            </div>
        `;
    }

    renderDetailList(details) {
        if (!Array.isArray(details) || details.length === 0) {
            return '';
        }

        return `
            <div class="component-details">
                ${details.map(([label, value, allowHTML]) => `
                    <div class="detail-item">
                        <span class="detail-label">${this.escapeHtml(label)}:</span>
                        <span class="detail-value">${allowHTML ? value : this.escapeHtml(String(value))}</span>
                    </div>
                `).join('')}
            </div>
        `;
    }

    renderDetourTag(target) {
        return `<span class="detour-tag" data-target="${this.escapeHtml(target)}">${this.escapeHtml(target)}</span>`;
    }

    attachGeoButtons() {
        document.querySelectorAll('.geo-update-btn').forEach((button) => {
            button.addEventListener('click', async (event) => {
                event.stopPropagation();
                const tag = button.getAttribute('data-tag');
                try {
                    const response = await fetch(`${this.baseUrl}/api/ip_router_action/${tag}?action=geoip_update`, {
                        method: 'POST'
                    });
                    if (!response.ok) {
                        throw new Error(await response.text());
                    }

                    const component = this.components.get(tag);
                    if (component) {
                        await this.fetchComponentDetails(component);
                        this.renderComponents();
                    }
                } catch (error) {
                    this.showError(`更新 GeoIP 失败: ${error.message}`);
                }
            });
        });
    }

    attachDetourClicks() {
        document.querySelectorAll('.detour-tag').forEach((tagElement) => {
            tagElement.addEventListener('click', (event) => {
                event.stopPropagation();
                const targetTag = tagElement.dataset.target;
                const targetComponent = this.components.get(targetTag);
                if (targetComponent) {
                    this.highlightDetourTargets(targetComponent);
                }
            });
        });
    }

    highlightDetourTargets(component) {
        this.clearHighlights();
        const targets = new Set();

        if (component.type === 'ip_router') {
            const detailed = this.detailedData.get(component.tag) || {};
            const rules = Array.isArray(detailed.rules) ? detailed.rules : [];
            rules.forEach((rule) => {
                const routeTargets = Array.isArray(rule.targets) ? rule.targets : [];
                routeTargets.forEach((target) => targets.add(target));
            });
            if (Array.isArray(component.detour_miss)) {
                component.detour_miss.forEach((target) => targets.add(target));
            }
        }

        if (component.detour) {
            if (Array.isArray(component.detour)) {
                if (component.detour.length > 0 && typeof component.detour[0] === 'object') {
                    component.detour.forEach((rule) => {
                        (rule.targets || []).forEach((target) => targets.add(target));
                    });
                } else {
                    component.detour.forEach((target) => targets.add(target));
                }
            } else if (typeof component.detour === 'object') {
                Object.values(component.detour).forEach((targetList) => {
                    (targetList || []).forEach((target) => targets.add(target));
                });
            }
        }

        if (component.type !== 'ip_router' && Array.isArray(component.detour_miss)) {
            component.detour_miss.forEach((target) => targets.add(target));
        }

        targets.forEach((target) => {
            const targetCard = document.querySelector(`[data-tag="${target}"]`);
            if (targetCard) {
                targetCard.classList.add('highlight');
            }
        });
    }

    clearHighlights() {
        document.querySelectorAll('.component-card.highlight').forEach((card) => {
            card.classList.remove('highlight');
        });
    }

    updateStatus(status, isOnline) {
        const statusElement = document.getElementById('connectionStatus');
        statusElement.textContent = status;
        statusElement.style.color = isOnline ? '#4caf50' : '#f44336';
    }

    showError(message) {
        const errorContainer = document.getElementById('errorContainer');
        errorContainer.innerHTML = `<div class="error-message">${this.escapeHtml(message)}</div>`;

        setTimeout(() => {
            errorContainer.innerHTML = '';
        }, 10000);
    }

    formatRate(bitsPerSec) {
        if (bitsPerSec >= 1024 * 1024 * 1024) {
            return `${(bitsPerSec / (1024 * 1024 * 1024)).toFixed(2)} Gbps`;
        }
        if (bitsPerSec >= 1024 * 1024) {
            return `${(bitsPerSec / (1024 * 1024)).toFixed(2)} Mbps`;
        }
        if (bitsPerSec >= 1024) {
            return `${(bitsPerSec / 1024).toFixed(2)} Kbps`;
        }
        return `${bitsPerSec} bps`;
    }

    formatBytes(bytes) {
        if (bytes >= 1024 * 1024 * 1024) {
            return `${(bytes / (1024 * 1024 * 1024)).toFixed(2)} GB`;
        }
        if (bytes >= 1024 * 1024) {
            return `${(bytes / (1024 * 1024)).toFixed(2)} MB`;
        }
        if (bytes >= 1024) {
            return `${(bytes / 1024).toFixed(2)} KB`;
        }
        return `${bytes} B`;
    }

    formatDateTime(value) {
        const date = new Date(value);
        if (Number.isNaN(date.getTime())) {
            return value;
        }
        return date.toLocaleString();
    }

    escapeHtml(value) {
        return String(value)
            .replaceAll('&', '&amp;')
            .replaceAll('<', '&lt;')
            .replaceAll('>', '&gt;')
            .replaceAll('"', '&quot;')
            .replaceAll("'", '&#39;');
    }
}

document.addEventListener('DOMContentLoaded', () => {
    new UDPlexMonitor();
});
