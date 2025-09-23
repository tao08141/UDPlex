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

        document.getElementById('intervalSelect').addEventListener('change', (e) => {
            this.refreshInterval = parseInt(e.target.value, 10);
            document.getElementById('refreshInterval').textContent = `${this.refreshInterval / 1000}秒`;
            if (this.autoRefresh) {
                this.startRefreshTimer();
            }
        });
    }

    toggleAutoRefresh() {
        this.autoRefresh = !this.autoRefresh;
        const btn = document.getElementById('autoRefreshBtn');
        btn.textContent = this.autoRefresh ? '自动刷新' : '手动刷新';
        btn.classList.toggle('active', this.autoRefresh);

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
            document.getElementById('connectionStatus').textContent = '加载组件...';
            // 只加载一次组件列表
            const response = await fetch(`${this.baseUrl}/api/components`);
            if (!response.ok) {
                throw new Error(`HTTP ${response.status}: ${response.statusText}`);
            }
            const components = await response.json();
            this.components.clear();
            for (const component of components) {
                this.components.set(component.tag, component);
                await this.fetchComponentDetails(component);
            }
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
            document.getElementById('connectionStatus').textContent = '更新中...';
            // 只刷新详细信息
            for (const component of this.components.values()) {
                await this.fetchComponentDetails(component);
            }
            // 记录当前高亮tag
            const highlighted = Array.from(document.querySelectorAll('.component-card.highlight'))
                .map(card => card.dataset.tag);
            this.renderComponents();
            // 恢复高亮
            highlighted.forEach(tag => {
                const card = document.querySelector(`[data-tag="${tag}"]`);
                if (card) card.classList.add('highlight');
            });
            this.updateStatus('在线', true);
            document.getElementById('lastUpdate').textContent = new Date().toLocaleTimeString();
        } catch (error) {
            console.error('Error fetching data:', error);
            this.showError(`连接失败: ${error.message}`);
            this.updateStatus('离线', false);
        }
    }

    async fetchComponentDetails(component) {
        try {
            let endpoint = '';
            switch (component.type) {
                case 'listen':
                    endpoint = `/api/listen/${component.tag}`;
                    break;
                case 'forward':
                    endpoint = `/api/forward/${component.tag}`;
                    break;
                case 'tcp_tunnel_listen':
                    endpoint = `/api/tcp_tunnel_listen/${component.tag}`;
                    break;
                case 'tcp_tunnel_forward':
                    endpoint = `/api/tcp_tunnel_forward/${component.tag}`;
                    break;
                case 'load_balancer':
                    endpoint = `/api/load_balancer/${component.tag}`;
                    break;
                case 'filter':
                    endpoint = `/api/filter/${component.tag}`;
                    break;
                case 'ip_router':
                    endpoint = `/api/ip_router/${component.tag}`;
                    break;
                default:
                    return;
            }

            const response = await fetch(`${this.baseUrl}${endpoint}`);
            if (response.ok) {
                const data = await response.json();
                this.detailedData.set(component.tag, data);
            }
        } catch (error) {
            console.warn(`Failed to fetch details for ${component.tag}:`, error);
        }
    }

    renderComponents() {
        const container = document.getElementById('componentsContainer');
        container.innerHTML = '';

        for (const [tag, component] of this.components) {
            const card = this.createComponentCard(component);
            container.appendChild(card);
        }

        document.getElementById('loadingContainer').style.display = 'none';
        container.style.display = 'grid';
        this.attachGeoButtons();
    }

    attachGeoButtons() {
        document.querySelectorAll('.geo-update-btn').forEach(btn => {
            btn.addEventListener('click', async (e) => {
                e.stopPropagation();
                const tag = btn.getAttribute('data-tag');
                try {
                    const res = await fetch(`${this.baseUrl}/api/ip_router_action/${tag}?action=geoip_update`, { method: 'POST' });
                    if (!res.ok) throw new Error(await res.text());
                    // refresh this component details
                    const comp = this.components.get(tag);
                    if (comp) {
                        await this.fetchComponentDetails(comp);
                        this.renderComponents();
                    }
                } catch (err) {
                    this.showError(`更新GeoIP失败: ${err.message}`);
                }
            });
        });
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
                <div class="component-tag">${component.tag}</div>
                <div class="component-type ${component.type}">${component.type}</div>
            </div>

            ${this.renderComponentDetails(component, detailedData)}
            ${this.renderDetourSection(component)}
            ${this.renderIPRouterActions(component, detailedData)}
            ${this.renderConnectionsInfo(detailedData)}
        `;

        return card;
    }

    // 新增：自适应速率单位
    formatRate(bitsPerSec) {
        if (bitsPerSec >= 1024 * 1024 * 1024) {
            return (bitsPerSec / (1024 * 1024 * 1024)).toFixed(2) + ' Gbps';
        } else if (bitsPerSec >= 1024 * 1024) {
            return (bitsPerSec / (1024 * 1024)).toFixed(2) + ' Mbps';
        } else if (bitsPerSec >= 1024) {
            return (bitsPerSec / 1024).toFixed(2) + ' Kbps';
        } else {
            return bitsPerSec + ' bps';
        }
    }

    renderComponentDetails(component, detailedData) {
        // ip_router 专属显示：规则与 GeoIP 状态
        if (component.type === 'ip_router' && detailedData) {
            const rules = Array.isArray(detailedData.rules) ? detailedData.rules : [];
            const miss = Array.isArray(detailedData.detour_miss) ? detailedData.detour_miss : (Array.isArray(component.detour_miss) ? component.detour_miss : []);
            const geo = detailedData.geoip || {};
            const ruleList = rules.map(r => `${r.match} → [${(r.targets||[]).join(', ')}]`).join('<br/>');
            const geoRows = [];
            if (typeof geo.db_loaded === 'boolean') geoRows.push(['GeoIP已加载', geo.db_loaded ? '是' : '否']);
            if (geo.geoip_url) geoRows.push(['GeoIP URL', geo.geoip_url]);
            if (geo.geoip_path) geoRows.push(['GeoIP 本地路径', geo.geoip_path]);
            if (geo.update_interval_sec) geoRows.push(['更新间隔', `${geo.update_interval_sec}s`]);
            let html = '<div class="component-details">';
            if (ruleList) {
                html += `
                <div class="detail-item"><span class="detail-label">规则:</span>
                    <span class="detail-value">${ruleList}</span></div>`;
            }
            if (miss && miss.length > 0) {
                html += `
                <div class=\"detail-item\"><span class=\"detail-label\">未匹配(Miss):</span>
                    <span class=\"detail-value\">[${miss.join(', ')}]</span></div>`;
            }
            geoRows.forEach(([label, value]) => {
                html += `
                <div class="detail-item"><span class="detail-label">${label}:</span>
                    <span class="detail-value">${value}</span></div>`;
            });
            html += '</div>';
            return html;
        }
        const details = [];

        // 基本配置信息
        if (component.listen_addr) {
            details.push(['监听地址', component.listen_addr]);
        }
        if (component.forwarders) {
            details.push(['转发目标', component.forwarders.join(', ')]);
        }
        if (component.timeout) {
            details.push(['超时时间', `${component.timeout}s`]);
        }
        if (component.window_size) {
            details.push(['窗口大小', component.window_size]);
        }
        if (component.use_proto_detectors) {
            details.push(['协议检测器', component.use_proto_detectors.join(', ')]);
        }

        // 连接统计信息
        if (detailedData) {
            if (detailedData.count !== undefined) {
                details.push(['连接数', detailedData.count]);
            }
            if (detailedData.total_connections !== undefined) {
                details.push(['总连接数', detailedData.total_connections]);
            }
            // 修改：load_balancer速率自适应单位
            if (detailedData.bits_per_sec !== undefined) {
                const rate = component.type === 'load_balancer'
                    ? this.formatRate(detailedData.bits_per_sec)
                    : ((detailedData.bits_per_sec / 1024).toFixed(2) + ' Kbps');
                details.push(['流量速率', rate]);
            }
            if (detailedData.packets_per_sec !== undefined) {
                details.push(['包速率', `${detailedData.packets_per_sec} pps`]);
            }
            // 平均延迟信息
            if (detailedData.average_delay_ms !== undefined && detailedData.average_delay_ms > 0) {
                details.push(['平均延迟', `${detailedData.average_delay_ms.toFixed(2)} ms`]);
            }
        }

        if (details.length === 0) {
            return '';
        }

        return `
            <div class="component-details">
                ${details.map(([label, value]) => `
                    <div class="detail-item">
                        <span class="detail-label">${label}:</span>
                        <span class="detail-value">${value}</span>
                    </div>
                `).join('')}
            </div>
        `;
    }

    renderDetourSection(component) {
        if (!component.detour) {
            return '';
        }

        let detourHtml = '<div class="detour-section"><div class="detour-title">路由目标 (Detour):</div>';

        if (Array.isArray(component.detour)) {
            if (component.detour.length > 0 && typeof component.detour[0] === 'object') {
                // 负载均衡规则
                component.detour.forEach(rule => {
                    detourHtml += `
                        <div class="detour-protocol">
                            <span class="protocol-label">规则: ${rule.rule}</span>
                            ${rule.targets.map(target => 
                                `<span class="detour-tag" data-target="${target}">${target}</span>`
                            ).join('')}
                        </div>
                    `;
                });
            } else {
                // 简单数组
                detourHtml += component.detour.map(target => 
                    `<span class="detour-tag" data-target="${target}">${target}</span>`
                ).join('');
            }
        } else if (typeof component.detour === 'object') {
            // 协议映射对象
            Object.entries(component.detour).forEach(([protocol, targets]) => {
                detourHtml += `
                    <div class="detour-protocol">
                        <span class="protocol-label">${protocol}:</span>
                        ${targets.map(target => 
                            `<span class="detour-tag" data-target="${target}">${target}</span>`
                        ).join('')}
                    </div>
                `;
            });
        }

        // detour_miss（优先展示详情接口中的 detour_miss，其次回退到组件配置）
        const missFromDetail = (this.detailedData.get(component.tag) || {}).detour_miss;
        const missArray = Array.isArray(missFromDetail) ? missFromDetail : (component.detour_miss || []);
        if (missArray && missArray.length > 0) {
            detourHtml += `
                <div class="detour-protocol">
                    <span class="protocol-label">未匹配:</span>
                    ${missArray.map(target => 
                        `<span class="detour-tag" data-target="${target}">${target}</span>`
                    ).join('')}
                </div>
            `;
        }

        detourHtml += '</div>';
        return detourHtml;
    }

    renderIPRouterActions(component, detailedData) {
        if (component.type !== 'ip_router') return '';
        const geo = detailedData && detailedData.geoip || {};
        const disabled = geo.geoip_url ? '' : 'disabled';
        return `
            <div class="detour-section">
                <div class="detour-title">IP Router 操作:</div>
                <button class="btn geo-update-btn" data-tag="${component.tag}" ${disabled}>更新GeoIP数据库</button>
            </div>
        `;
    }

    renderConnectionsInfo(detailedData) {
        if (!detailedData || (!detailedData.connections && !detailedData.pools)) {
            return '';
        }

        let html = '<div class="connections-info"><div class="detour-title">连接信息:</div>';

        if (detailedData.connections) {
            detailedData.connections.forEach(conn => {
                const status = conn.is_connected !== undefined ? 
                    (conn.is_connected ? 'online' : 'offline') : 
                    (conn.is_authenticated ? 'online' : 'warning');
                
                html += `
                    <div class="connection-item">
                        <span class="status-indicator status-${status}"></span>
                        ${conn.address || conn.remote_addr}
                        ${conn.last_active ? ` (${new Date(conn.last_active).toLocaleTimeString()})` : ''}
                    </div>
                `;
            });
        }

        if (detailedData.pools) {
            detailedData.pools.forEach(pool => {
                html += `
                    <div class="connection-item">
                        <strong>池 ${pool.pool_id}:</strong> ${pool.conn_count} 连接
                        ${pool.target_count ? ` (目标: ${pool.target_count})` : ''}
                    </div>
                `;
                // 展示该池的所有连接
                if (pool.connections && pool.connections.length > 0) {
                    pool.connections.forEach(conn => {
                        const status = conn.is_connected !== undefined
                            ? (conn.is_connected ? 'online' : 'offline')
                            : (conn.is_authenticated ? 'online' : 'warning');
                        html += `
                            <div class="connection-item" style="margin-left: 24px;">
                                <span class="status-indicator status-${status}"></span>
                                ${conn.address || conn.remote_addr}
                                ${conn.last_active ? ` (${new Date(conn.last_active).toLocaleTimeString()})` : ''}
                            </div>
                        `;
                    });
                }
            });
        }

        html += '</div>';
        return html;
    }

    highlightDetourTargets(component) {
        this.clearHighlights();

        const targets = new Set();

        // 1) ip_router：将规则里的 targets + detour_miss 一并高亮
        if (component.type === 'ip_router') {
            const detailed = this.detailedData.get(component.tag) || {};
            const rules = Array.isArray(detailed.rules) ? detailed.rules : [];
            rules.forEach(r => {
                const tgs = Array.isArray(r.targets) ? r.targets : [];
                tgs.forEach(t => targets.add(t));
            });
            if (Array.isArray(component.detour_miss)) {
                component.detour_miss.forEach(t => targets.add(t));
            }
        }

        // 2) 通用 detour 高亮逻辑（保留原有，兼容其他组件类型）
        if (component.detour) {
            if (Array.isArray(component.detour)) {
                if (component.detour.length > 0 && typeof component.detour[0] === 'object') {
                    // 负载均衡规则
                    component.detour.forEach(rule => {
                        rule.targets.forEach(target => targets.add(target));
                    });
                } else {
                    // 简单数组
                    component.detour.forEach(target => targets.add(target));
                }
            } else if (typeof component.detour === 'object') {
                // 协议映射对象
                Object.values(component.detour).forEach(targetList => {
                    targetList.forEach(target => targets.add(target));
                });
            }
        }

        // detour_miss（非 ip_router 的场景依然适用）
        if (component.type !== 'ip_router' && component.detour_miss) {
            component.detour_miss.forEach(target => targets.add(target));
        }

        // 高亮目标组件
        targets.forEach(target => {
            const targetCard = document.querySelector(`[data-tag="${target}"]`);
            if (targetCard) {
                targetCard.classList.add('highlight');
            }
        });

        // 添加detour标签点击事件
        const detourTags = document.querySelectorAll('.detour-tag');
        detourTags.forEach(tag => {
            tag.addEventListener('click', (e) => {
                e.stopPropagation();
                const targetTag = tag.dataset.target;
                const targetComponent = this.components.get(targetTag);
                if (targetComponent) {
                    this.highlightDetourTargets(targetComponent);
                }
            });
        });
    }

    clearHighlights() {
        const highlighted = document.querySelectorAll('.component-card.highlight');
        highlighted.forEach(card => {
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
        errorContainer.innerHTML = `<div class="error-message">${message}</div>`;
        
        setTimeout(() => {
            errorContainer.innerHTML = '';
        }, 10000);
    }
}

// 初始化监控系统
document.addEventListener('DOMContentLoaded', () => {
    new UDPlexMonitor();
});
