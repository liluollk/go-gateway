// router 包实现网关的路由分发逻辑，支持权重轮询和故障转移。
// Phase 2 实现：权重轮询 + fallback 链，替换 handler 中的 findProvider 简单查找。
package router

import (
	"sync/atomic"

	"go-gateway/internal/config"
)

// Router 是路由分发器，根据配置的路由规则选择目标供应商。
// 支持同一模型配置多个供应商，按权重分配流量，主供应商失败时自动 fallback。
type Router struct {
	cfg     *config.Config
	counter atomic.Uint64 // 全局递增计数器，用于权重轮询
}

// NewRouter 创建路由实例。
func NewRouter(cfg *config.Config) *Router {
	return &Router{cfg: cfg}
}

// SelectProvider 根据模型名选择目标供应商配置。
// 策略：
// 1. 查找匹配的 route 规则
// 2. 按权重轮询选择目标供应商
// 3. 返回命中的 ProviderConfig，nil 表示无可用供应商
func (r *Router) SelectProvider(model string) *config.ProviderConfig {
	route := r.findRoute(model)
	if route == nil {
		return nil
	}

	// 单供应商：直接返回
	if len(route.Providers) == 1 {
		return r.findProviderByID(route.Providers[0].ProviderID)
	}

	// 多供应商：权重轮询
	target := r.weightedSelect(route.Providers)
	if target == nil {
		return nil
	}
	return r.findProviderByID(target.ProviderID)
}

// GetFallbackProviders 返回除主供应商外的 fallback 候选列表。
// excludeProviderID 是已选中的主供应商 ID，从该列表中排除。
// 调用方在主供应商失败后，依次尝试 fallback 链中的供应商。
func (r *Router) GetFallbackProviders(model string, excludeProviderID string) []*config.ProviderConfig {
	route := r.findRoute(model)
	if route == nil || len(route.Providers) <= 1 {
		return nil
	}

	var fallbacks []*config.ProviderConfig
	for _, t := range route.Providers {
		if t.ProviderID == excludeProviderID {
			continue
		}
		if p := r.findProviderByID(t.ProviderID); p != nil {
			fallbacks = append(fallbacks, p)
		}
	}
	return fallbacks
}

// findRoute 根据模型名查找匹配的路由规则。
func (r *Router) findRoute(model string) *config.RouteConfig {
	for _, route := range r.cfg.Routes {
		if route.Model == model {
			return &route
		}
	}
	return nil
}

// findProviderByID 根据 provider ID 查找 ProviderConfig。
func (r *Router) findProviderByID(id string) *config.ProviderConfig {
	for i := range r.cfg.Providers {
		if r.cfg.Providers[i].ID == id {
			return &r.cfg.Providers[i]
		}
	}
	return nil
}

// weightedSelect 按权重轮询选择一个 RouteTarget。
// 使用全局递增计数器实现平滑加权轮询。
func (r *Router) weightedSelect(targets []config.RouteTarget) *config.RouteTarget {
	if len(targets) == 0 {
		return nil
	}

	// 计算总权重
	totalWeight := 0
	for _, t := range targets {
		if t.Weight <= 0 {
			totalWeight += 1 // 默认权重 1
		} else {
			totalWeight += t.Weight
		}
	}

	// 取模得到位置
	pos := int(r.counter.Add(1)) % totalWeight

	// 找到对应位置的目标
	current := 0
	for i := range targets {
		w := targets[i].Weight
		if w <= 0 {
			w = 1
		}
		current += w
		if pos < current {
			return &targets[i]
		}
	}

	// 回退到第一个
	return &targets[0]
}