/**
 * @license MIT
 * ExecutiveDashboard Component Suite — Morphic Design System
 * Comprehensive Multi-Domain Executive Analytics & Operations Center
 * 
 * Copyright (c) 2026 Morphic Design System Contributors
 */

import React, { useState } from 'react';
import { motion } from 'motion/react';
import { Icon } from '../components/communication/Icon.js';
import { Button } from '../components/actions/Button.js';
import { Chip, DatePickerModal } from '../components/selection/index.js';

export interface DashboardTransaction {
  id: string;
  entity: string;
  category: string;
  amount: string;
  timestamp: string;
  status: 'Completed' | 'Processing' | 'Pending' | 'Failed';
  avatar: string;
}

export interface ActivityItem {
  id: string;
  user: string;
  action: string;
  target: string;
  time: string;
  icon: string;
  iconBg: string;
}

export const ExecutiveDashboard: React.FC<{
  onExportExcel?: () => void;
  onExportPdf?: () => void;
  onNewReport?: () => void;
  onViewDetails?: (item: DashboardTransaction) => void;
}> = ({ onExportExcel, onExportPdf, onNewReport, onViewDetails }) => {
  const [timeRange, setTimeRange] = useState('30 Days');
  const [searchQuery, setSearchQuery] = useState('');
  const [selectedStatus, setSelectedStatus] = useState('All');
  const [isDatePickerOpen, setIsDatePickerOpen] = useState(false);
  const [selectedDate, setSelectedDate] = useState<Date>(new Date(2026, 7, 27));

  // Sample Transaction / Entity Data
  const transactions: DashboardTransaction[] = [
    {
      id: 'TRX-98214',
      entity: 'Enterprise Cloud Infrastructure Tier-3',
      category: 'Infrastructure',
      amount: '$48,500.00',
      timestamp: 'Aug 27, 2026, 14:15',
      status: 'Completed',
      avatar: 'https://images.unsplash.com/photo-1534528741775-53994a69daeb?w=100&auto=format&fit=crop&q=80',
    },
    {
      id: 'TRX-98215',
      entity: 'Global Content Delivery Network Sync',
      category: 'Networking',
      amount: '$16,200.00',
      timestamp: 'Aug 27, 2026, 13:40',
      status: 'Processing',
      avatar: 'https://images.unsplash.com/photo-1507003211169-0a1dd7228f2d?w=100&auto=format&fit=crop&q=80',
    },
    {
      id: 'TRX-98216',
      entity: 'Automated Cyber Security Audit Suite',
      category: 'Security',
      amount: '$32,800.00',
      timestamp: 'Aug 27, 2026, 11:20',
      status: 'Completed',
      avatar: 'https://images.unsplash.com/photo-1494790108377-be9c29b29330?w=100&auto=format&fit=crop&q=80',
    },
    {
      id: 'TRX-98217',
      entity: 'AI Neural Inference API Allocation',
      category: 'Artificial Intelligence',
      amount: '$74,350.00',
      timestamp: 'Aug 26, 2026, 19:05',
      status: 'Pending',
      avatar: 'https://images.unsplash.com/photo-1517841905240-472988babdf9?w=100&auto=format&fit=crop&q=80',
    },
    {
      id: 'TRX-98218',
      entity: 'Multi-Region High Availability Cluster',
      category: 'Infrastructure',
      amount: '$58,900.00',
      timestamp: 'Aug 26, 2026, 16:30',
      status: 'Completed',
      avatar: 'https://images.unsplash.com/photo-1534528741775-53994a69daeb?w=100&auto=format&fit=crop&q=80',
    },
  ];

  // Sample Activity Stream
  const activities: ActivityItem[] = [
    {
      id: 'act-1',
      user: 'Alexander Pratama',
      action: 'published gateway configuration update to',
      target: 'Production Cluster-East',
      time: '5 mins ago',
      icon: 'sync',
      iconBg: 'var(--md-sys-color-success-container)',
    },
    {
      id: 'act-2',
      user: 'Nadia Salsabila',
      action: 'approved compute quota allocation for',
      target: 'Q3 Enterprise Target',
      time: '28 mins ago',
      icon: 'verified',
      iconBg: 'var(--md-sys-color-info-container)',
    },
    {
      id: 'act-3',
      user: 'Automated System',
      action: 'completed daily snapshot vault backup for',
      target: 'Vault Backup 04',
      time: '2 hours ago',
      icon: 'cloud_done',
      iconBg: 'var(--md-sys-color-warning-container)',
    },
    {
      id: 'act-4',
      user: 'Dimas Wicaksono',
      action: 'renewed SSL authentication certificate for',
      target: 'api.morphic-global.io',
      time: '4 hours ago',
      icon: 'security',
      iconBg: 'var(--md-sys-color-error-container)',
    },
  ];

  const filteredTransactions = transactions.filter((t) => {
    const matchesSearch =
      t.entity.toLowerCase().includes(searchQuery.toLowerCase()) ||
      t.id.toLowerCase().includes(searchQuery.toLowerCase());
    const matchesStatus = selectedStatus === 'All' || t.status === selectedStatus;
    return matchesSearch && matchesStatus;
  });

  const getStatusBadge = (status: DashboardTransaction['status']) => {
    switch (status) {
      case 'Completed':
        return { bg: 'var(--md-sys-color-success-container)', text: 'var(--md-sys-color-primary)', label: 'Completed' };
      case 'Processing':
        return { bg: 'var(--md-sys-color-info-container)', text: 'var(--md-sys-color-info)', label: 'Processing' };
      case 'Pending':
        return { bg: 'var(--md-sys-color-warning-container)', text: 'var(--md-sys-color-warning)', label: 'Pending' };
      case 'Failed':
        return { bg: 'var(--md-sys-color-error-container)', text: 'var(--md-sys-color-error)', label: 'Failed' };
    }
  };

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: '28px' }}>
      {/* 1. Header Filter & Live Sync Bar */}
      <div
        style={{
          display: 'flex',
          justifyContent: 'space-between',
          alignItems: 'center',
          flexWrap: 'wrap',
          gap: '16px',
        }}
      >
        <div style={{ display: 'flex', gap: '8px', alignItems: 'center', flexWrap: 'wrap' }}>
          <span style={{ fontSize: '13px', fontWeight: 600, color: 'var(--md-sys-color-on-surface-variant)', marginRight: '4px' }}>
            Time Period:
          </span>
          {['Today', '7 Days', '30 Days', 'This Quarter'].map((range) => (
            <Chip
              key={range}
              variant="filter"
              selected={timeRange === range}
              onClick={() => setTimeRange(range)}
            >
              {range}
            </Chip>
          ))}

          <Button
            variant={timeRange === 'Custom' ? 'filled' : 'outlined'}
            size="sm"
            icon={<Icon name="calendar_month" size={16} />}
            onClick={() => setIsDatePickerOpen(true)}
          >
            {timeRange === 'Custom'
              ? `${selectedDate.getDate()} Aug 2026`
              : 'Select Date...'}
          </Button>
        </div>

        <DatePickerModal
          isOpen={isDatePickerOpen}
          onClose={() => setIsDatePickerOpen(false)}
          selectedDate={selectedDate}
          onSelect={(d) => {
            setSelectedDate(d);
            setTimeRange('Custom');
          }}
        />

        <div style={{ display: 'flex', alignItems: 'center', gap: '12px' }}>
          <div
            style={{
              display: 'flex',
              alignItems: 'center',
              gap: '6px',
              padding: '6px 12px',
              borderRadius: 'var(--radius-pill)',
              backgroundColor: 'var(--md-sys-color-surface-container)',
              fontSize: '12px',
              fontWeight: 600,
              color: 'var(--md-sys-color-primary)',
            }}
          >
            <span style={{ width: '8px', height: '8px', borderRadius: '50%', backgroundColor: 'var(--md-sys-color-primary)', display: 'inline-block' }} />
            <span>Live Sync 2.4s</span>
          </div>

          <Button variant="tonal" size="sm" icon={<Icon name="refresh" size={16} />}>
            Refresh
          </Button>
        </div>
      </div>

      {/* 2. Dynamic Organic M3 Morphic Hero Banner */}
      <div
        style={{
          borderRadius: 'var(--radius-hero)', // 28px
          padding: '30px 38px',
          background: 'var(--hero-banner-bg)',
          color: 'var(--hero-banner-text)',
          position: 'relative',
          overflow: 'hidden',
          boxShadow: 'var(--md-sys-elevation-level2)',
          display: 'flex',
          justifyContent: 'space-between',
          alignItems: 'center',
          minHeight: '140px',
          transition: 'background 0.25s ease, color 0.25s ease',
        }}
      >
        {/* Background Organic Decorative Art (Clean: curved arc line removed) */}
        <svg
          style={{
            position: 'absolute',
            top: 0,
            right: 0,
            width: '100%',
            height: '100%',
            pointerEvents: 'none',
            zIndex: 0,
          }}
          viewBox="0 0 900 160"
          preserveAspectRatio="xMaxYMid slice"
          fill="none"
        >
          <path d="M 120 120 C 130 90, 160 85, 185 88 L 195 160 L 110 160 Z" fill="var(--hero-banner-blob-1)" opacity="0.5" />
          <rect x="370" y="80" width="54" height="90" rx="27" fill="var(--hero-banner-blob-1)" opacity="0.6" />
          <rect x="460" y="-20" width="220" height="150" rx="75" fill="var(--hero-banner-blob-2)" opacity="0.55" />
          <rect x="440" y="14" width="180" height="86" rx="43" fill="none" stroke="var(--hero-banner-arc)" strokeWidth="3.5" />
          <circle cx="670" cy="50" r="22" fill="none" stroke="var(--hero-banner-arc)" strokeWidth="3.5" />
          <path d="M 720 50 C 720 35, 740 30, 755 35 C 770 25, 790 35, 795 50 C 810 55, 815 75, 805 90 C 815 105, 805 125, 790 130 C 775 140, 755 135, 745 125 C 730 135, 715 120, 715 105 C 700 95, 705 75, 715 65 Z" fill="var(--hero-banner-blob-2)" opacity="0.5" />
          <circle cx="860" cy="120" r="70" fill="var(--hero-banner-blob-1)" opacity="0.4" />
        </svg>

        {/* Left Content Area */}
        <div style={{ position: 'relative', zIndex: 2, maxWidth: '620px' }}>
          <div style={{ display: 'flex', alignItems: 'center', gap: '8px', marginBottom: '4px' }}>
            <div
              style={{
                width: '24px',
                height: '24px',
                borderRadius: 'var(--radius-xs)',
                backgroundColor: 'var(--hero-banner-tag-bg)',
                color: 'var(--hero-banner-tag-text)',
                display: 'flex',
                alignItems: 'center',
                justifyContent: 'center',
                fontSize: '14px',
                transition: 'all 0.25s ease',
              }}
            >
              <Icon name="hub" size={15} />
            </div>
            <span
              style={{
                fontSize: '11px',
                fontWeight: 600,
                color: 'var(--hero-banner-subtext)',
                textTransform: 'uppercase',
                letterSpacing: '0.06em',
              }}
            >
              WORKSPACE OVERVIEW
            </span>
          </div>

          <h2
            style={{
              margin: '2px 0 0',
              fontSize: '28px',
              fontWeight: 700,
              letterSpacing: '-0.02em',
              color: 'var(--hero-banner-text)',
              lineHeight: 1.2,
            }}
          >
            Good morning, Alex
          </h2>
        </div>

        {/* Right Action Pills */}
        <div
          style={{
            position: 'relative',
            zIndex: 2,
            display: 'flex',
            alignItems: 'center',
            gap: '10px',
            alignSelf: 'flex-end',
            marginBottom: '4px',
          }}
        >
          <motion.button
            whileHover={{ scale: 1.05, y: -2 }}
            whileTap={{ scale: 0.95 }}
            onClick={onExportExcel}
            style={{
              height: '38px',
              padding: '0 18px',
              borderRadius: 'var(--radius-pill)',
              border: 'none',
              backgroundColor: 'var(--hero-banner-pill-bg)',
              color: 'var(--hero-banner-pill-text)',
              fontSize: '12px',
              fontWeight: 600,
              display: 'inline-flex',
              alignItems: 'center',
              gap: '6px',
              cursor: 'pointer',
              boxShadow: 'var(--md-sys-elevation-level1)',
              transition: 'background-color 0.25s ease, color 0.25s ease',
            }}
          >
            <Icon name="table_chart" size={16} color="var(--hero-banner-pill-icon)" />
            <span>Export Excel</span>
          </motion.button>

          <motion.button
            whileHover={{ scale: 1.05, y: -2 }}
            whileTap={{ scale: 0.95 }}
            onClick={onExportPdf}
            style={{
              height: '38px',
              padding: '0 18px',
              borderRadius: 'var(--radius-pill)',
              border: 'none',
              backgroundColor: 'var(--hero-banner-pill-bg)',
              color: 'var(--hero-banner-pill-text)',
              fontSize: '12px',
              fontWeight: 600,
              display: 'inline-flex',
              alignItems: 'center',
              gap: '6px',
              cursor: 'pointer',
              boxShadow: 'var(--md-sys-elevation-level1)',
              transition: 'background-color 0.25s ease, color 0.25s ease',
            }}
          >
            <Icon name="picture_as_pdf" size={16} color="var(--hero-banner-pill-icon)" />
            <span>Download PDF</span>
          </motion.button>
        </div>
      </div>

      {/* 3. Universal KPI Cards Grid with Micro-Progress */}
      <div style={{ display: 'grid', gridTemplateColumns: 'repeat(4, 1fr)', gap: '16px' }}>
        {[
          { label: 'Total Activity Volume', value: '248,500 Data', delta: '+18.2%', isPos: true, icon: 'trending_up', progress: 78 },
          { label: 'Active Workspace Users', value: '84,250 Users', delta: '+12.4%', isPos: true, icon: 'group', progress: 64 },
          { label: 'System Compliance Rate', value: '99.4%', delta: '+0.6%', isPos: true, icon: 'verified', progress: 99 },
          { label: 'Average Response Time', value: '142 ms', delta: '-18ms Fast', isPos: true, icon: 'speed', progress: 85 },
        ].map((kpi, idx) => (
          <div
            key={idx}
            style={{
              borderRadius: 'var(--radius-xl)',
              backgroundColor: 'var(--md-sys-color-surface)',
              border: '1px solid var(--md-sys-color-border)',
              padding: '20px 24px',
              boxShadow: 'var(--md-sys-elevation-level1)',
              display: 'flex',
              flexDirection: 'column',
              justifyContent: 'space-between',
            }}
          >
            <div>
              <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
                <span style={{ fontSize: '12px', color: 'var(--md-sys-color-on-surface-variant)', fontWeight: 500 }}>
                  {kpi.label}
                </span>
                <div
                  style={{
                    width: '32px',
                    height: '32px',
                    borderRadius: 'var(--radius-sm)',
                    backgroundColor: 'var(--md-sys-color-surface-container)',
                    color: 'var(--md-sys-color-primary)',
                    display: 'flex',
                    alignItems: 'center',
                    justifyContent: 'center',
                  }}
                >
                  <Icon name={kpi.icon} size={18} />
                </div>
              </div>
              <div style={{ fontSize: '22px', fontWeight: 700, marginTop: '10px', letterSpacing: '-0.02em', fontFeatureSettings: '"tnum" 1' }}>
                {kpi.value}
              </div>
              <div style={{ fontSize: '11px', color: 'var(--md-sys-color-primary)', fontWeight: 600, marginTop: '4px' }}>
                {kpi.delta} vs last week
              </div>
            </div>

            {/* Micro Progress Bar */}
            <div style={{ marginTop: '16px', height: '4px', borderRadius: 'var(--radius-pill)', backgroundColor: 'var(--md-sys-color-surface-container)', overflow: 'hidden' }}>
              <div
                style={{
                  width: `${kpi.progress}%`,
                  height: '100%',
                  borderRadius: 'var(--radius-pill)',
                  backgroundColor: 'var(--md-sys-color-primary)',
                }}
              />
            </div>
          </div>
        ))}
      </div>

      {/* 4. Analytics Chart & Resource Gauges Split */}
      <div style={{ display: 'grid', gridTemplateColumns: '1.6fr 1fr', gap: '20px' }}>
        {/* Line Trend Chart */}
        <div
          style={{
            borderRadius: 'var(--radius-xl)',
            backgroundColor: 'var(--md-sys-color-surface)',
            border: '1px solid var(--md-sys-color-border)',
            padding: '24px 28px',
            boxShadow: 'var(--md-sys-elevation-level1)',
          }}
        >
          <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '16px' }}>
            <div>
              <h3 style={{ margin: 0, fontSize: '17px', fontWeight: 600 }}>Activity Growth Trend</h3>
              <div style={{ fontSize: '12px', color: 'var(--md-sys-color-on-surface-variant)' }}>System traffic & transactions over last 7 days</div>
            </div>
            <span style={{ fontSize: '13px', fontWeight: 700, color: 'var(--md-sys-color-primary)' }}>+24.5% Avg</span>
          </div>

          <div style={{ width: '100%', height: '170px' }}>
            <svg width="100%" height="100%" viewBox="0 0 500 170" preserveAspectRatio="none">
              <line x1="20" y1="40" x2="480" y2="40" stroke="var(--md-sys-color-border)" strokeDasharray="4 4" />
              <line x1="20" y1="90" x2="480" y2="90" stroke="var(--md-sys-color-border)" strokeDasharray="4 4" />
              <line x1="20" y1="140" x2="480" y2="140" stroke="var(--md-sys-color-border)" />
              <motion.path
                d="M 30 130 Q 100 80, 180 100 T 320 50 T 470 30"
                fill="none"
                stroke="var(--md-sys-color-primary)"
                strokeWidth="3.5"
                strokeLinecap="round"
                initial={{ pathLength: 0 }}
                animate={{ pathLength: 1 }}
                transition={{ duration: 1.2 }}
              />
            </svg>
          </div>
        </div>

        {/* Capacity & Quota Gauges */}
        <div
          style={{
            borderRadius: 'var(--radius-xl)',
            backgroundColor: 'var(--md-sys-color-surface)',
            border: '1px solid var(--md-sys-color-border)',
            padding: '24px 28px',
            boxShadow: 'var(--md-sys-elevation-level1)',
            display: 'flex',
            flexDirection: 'column',
            justifyContent: 'space-between',
          }}
        >
          <div>
            <h3 style={{ margin: '0 0 4px', fontSize: '17px', fontWeight: 600 }}>System Allocation & Capacity</h3>
            <div style={{ fontSize: '12px', color: 'var(--md-sys-color-on-surface-variant)', marginBottom: '16px' }}>Active resource quota status</div>

            <div style={{ display: 'flex', flexDirection: 'column', gap: '14px' }}>
              <div>
                <div style={{ display: 'flex', justifyContent: 'space-between', fontSize: '12px', marginBottom: '4px' }}>
                  <span>Cloud CPU Compute</span>
                  <strong>68% Utilized</strong>
                </div>
                <div style={{ height: '8px', borderRadius: 'var(--radius-pill)', backgroundColor: 'var(--md-sys-color-surface-container)', overflow: 'hidden' }}>
                  <div style={{ width: '68%', height: '100%', borderRadius: 'var(--radius-pill)', backgroundColor: 'var(--md-sys-color-primary)' }} />
                </div>
              </div>

              <div>
                <div style={{ display: 'flex', justifyContent: 'space-between', fontSize: '12px', marginBottom: '4px' }}>
                  <span>Encrypted Database Storage</span>
                  <strong>42% (2.1 TB / 5 TB)</strong>
                </div>
                <div style={{ height: '8px', borderRadius: 'var(--radius-pill)', backgroundColor: 'var(--md-sys-color-surface-container)', overflow: 'hidden' }}>
                  <div style={{ width: '42%', height: '100%', borderRadius: 'var(--radius-pill)', backgroundColor: 'var(--md-sys-color-info)' }} />
                </div>
              </div>

              <div>
                <div style={{ display: 'flex', justifyContent: 'space-between', fontSize: '12px', marginBottom: '4px' }}>
                  <span>API Gateway Quota Limit</span>
                  <strong>89% (High Load)</strong>
                </div>
                <div style={{ height: '8px', borderRadius: 'var(--radius-pill)', backgroundColor: 'var(--md-sys-color-surface-container)', overflow: 'hidden' }}>
                  <div style={{ width: '89%', height: '100%', borderRadius: 'var(--radius-pill)', backgroundColor: 'var(--md-sys-color-warning)' }} />
                </div>
              </div>
            </div>
          </div>

          <div style={{ marginTop: '16px', paddingTop: '12px', borderTop: '1px solid var(--md-sys-color-border)', display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
            <span style={{ fontSize: '12px', color: 'var(--md-sys-color-on-surface-variant)' }}>Cluster Health: <strong>99.98% Healthy</strong></span>
            <Button variant="text" size="sm" icon={<Icon name="tune" size={14} />}>Configure</Button>
          </div>
        </div>
      </div>

      {/* 5. Interactive Operations Table & Recent Transactions */}
      <div
        style={{
          borderRadius: 'var(--radius-xl)',
          backgroundColor: 'var(--md-sys-color-surface)',
          border: '1px solid var(--md-sys-color-border)',
          overflow: 'hidden',
          boxShadow: 'var(--md-sys-elevation-level1)',
        }}
      >
        {/* Table Header Controls */}
        <div
          style={{
            padding: '20px 24px',
            borderBottom: '1px solid var(--md-sys-color-border)',
            display: 'flex',
            justifyContent: 'space-between',
            alignItems: 'center',
            flexWrap: 'wrap',
            gap: '16px',
          }}
        >
          <div>
            <h3 style={{ margin: '0 0 4px', fontSize: '17px', fontWeight: 600 }}>
              Transactions & Entity Activity
            </h3>
            <div style={{ fontSize: '12px', color: 'var(--md-sys-color-on-surface-variant)' }}>
              Showing {filteredTransactions.length} latest transaction entities
            </div>
          </div>

          <div style={{ display: 'flex', alignItems: 'center', gap: '12px' }}>
            {/* Search Input Box */}
            <div
              style={{
                display: 'flex',
                alignItems: 'center',
                gap: '8px',
                padding: '6px 14px',
                borderRadius: 'var(--radius-pill)',
                backgroundColor: 'var(--md-sys-color-surface-container)',
                border: '1px solid var(--md-sys-color-border)',
                minWidth: '220px',
              }}
            >
              <Icon name="search" size={16} color="var(--md-sys-color-on-surface-variant)" />
              <input
                type="text"
                placeholder="Search transaction ID / entity..."
                value={searchQuery}
                onChange={(e) => setSearchQuery(e.target.value)}
                style={{
                  border: 'none',
                  outline: 'none',
                  backgroundColor: 'transparent',
                  color: 'var(--md-sys-color-on-surface)',
                  fontSize: '12px',
                  width: '100%',
                }}
              />
            </div>

            {/* Status Filter Dropdown */}
            <div style={{ display: 'flex', gap: '6px' }}>
              {['All', 'Completed', 'Processing', 'Pending'].map((st) => (
                <Chip
                  key={st}
                  variant="filter"
                  selected={selectedStatus === st}
                  onClick={() => setSelectedStatus(st)}
                >
                  {st}
                </Chip>
              ))}
            </div>
          </div>
        </div>

        {/* Data Rows */}
        <div>
          <div
            style={{
              display: 'grid',
              gridTemplateColumns: '1.2fr 2fr 1.2fr 1.4fr 1fr 1fr',
              padding: '12px 24px',
              backgroundColor: 'var(--md-sys-color-surface-container-low)',
              fontSize: '11px',
              fontWeight: 600,
              color: 'var(--md-sys-color-on-surface-variant)',
              textTransform: 'uppercase',
              letterSpacing: '0.04em',
              borderBottom: '1px solid var(--md-sys-color-border)',
            }}
          >
            <span>Transaction ID</span>
            <span>Entity / Project</span>
            <span>Category</span>
            <span>Volume</span>
            <span>Status</span>
            <span style={{ textAlign: 'right' }}>Actions</span>
          </div>

          {filteredTransactions.map((t) => {
            const badge = getStatusBadge(t.status);
            return (
              <div
                key={t.id}
                style={{
                  display: 'grid',
                  gridTemplateColumns: '1.2fr 2fr 1.2fr 1.4fr 1fr 1fr',
                  padding: '16px 24px',
                  alignItems: 'center',
                  borderBottom: '1px solid var(--md-sys-color-border)',
                  fontSize: '13px',
                  transition: 'background-color 0.15s ease',
                }}
              >
                <span style={{ fontWeight: 700, fontFeatureSettings: '"tnum" 1' }}>{t.id}</span>

                <div style={{ display: 'flex', alignItems: 'center', gap: '10px' }}>
                  <img
                    src={t.avatar}
                    alt={t.entity}
                    style={{ width: '28px', height: '28px', borderRadius: '50%', objectFit: 'cover' }}
                  />
                  <div>
                    <div style={{ fontWeight: 600 }}>{t.entity}</div>
                    <div style={{ fontSize: '11px', color: 'var(--md-sys-color-on-surface-variant)' }}>{t.timestamp}</div>
                  </div>
                </div>

                <span style={{ color: 'var(--md-sys-color-on-surface-variant)' }}>{t.category}</span>

                <span style={{ fontWeight: 700, fontFeatureSettings: '"tnum" 1' }}>{t.amount}</span>

                <div>
                  <span
                    style={{
                      padding: '4px 10px',
                      borderRadius: 'var(--radius-pill)',
                      backgroundColor: badge.bg,
                      color: badge.text,
                      fontSize: '11px',
                      fontWeight: 600,
                    }}
                  >
                    {badge.label}
                  </span>
                </div>

                <div style={{ display: 'flex', justifyContent: 'flex-end', gap: '8px' }}>
                  <Button variant="tonal" size="sm" onClick={() => onViewDetails && onViewDetails(t)}>
                    Details
                  </Button>
                </div>
              </div>
            );
          })}
        </div>

        {/* Table Pagination Bar */}
        <div
          style={{
            padding: '14px 24px',
            display: 'flex',
            justifyContent: 'space-between',
            alignItems: 'center',
            backgroundColor: 'var(--md-sys-color-surface)',
            fontSize: '12px',
            color: 'var(--md-sys-color-on-surface-variant)',
          }}
        >
          <span>Showing 1-5 of 24,850 entity records</span>
          <div style={{ display: 'flex', gap: '8px' }}>
            <Button variant="outlined" size="sm">Previous</Button>
            <Button variant="filled" size="sm">Next</Button>
          </div>
        </div>
      </div>

      {/* 6. Operations Shortcuts & Real-time Activity Stream Grid */}
      <div style={{ display: 'grid', gridTemplateColumns: '1fr 1.6fr', gap: '20px' }}>
        {/* Quick Actions Panel */}
        <div
          style={{
            borderRadius: 'var(--radius-xl)',
            backgroundColor: 'var(--md-sys-color-surface)',
            border: '1px solid var(--md-sys-color-border)',
            padding: '24px',
            boxShadow: 'var(--md-sys-elevation-level1)',
          }}
        >
          <h3 style={{ margin: '0 0 16px', fontSize: '17px', fontWeight: 600 }}>Quick Operations</h3>
          <div style={{ display: 'grid', gridTemplateColumns: 'repeat(2, 1fr)', gap: '12px' }}>
            {[
              { label: 'Create New Report', icon: 'post_add', desc: 'Schedule automated export' },
              { label: 'Add Cluster Node', icon: 'dns', desc: 'Expand server capacity' },
              { label: 'Backup Vault', icon: 'cloud_upload', desc: 'Manual encrypted snapshot' },
              { label: 'Audit API Keys', icon: 'key', desc: 'Rotate team access tokens' },
            ].map((act, i) => (
              <motion.button
                key={i}
                whileHover={{ y: -3, scale: 1.02 }}
                whileTap={{ scale: 0.98 }}
                onClick={onNewReport}
                style={{
                  padding: '16px',
                  borderRadius: 'var(--radius-lg)',
                  backgroundColor: 'var(--md-sys-color-surface-container)',
                  border: '1px solid var(--md-sys-color-border)',
                  color: 'var(--md-sys-color-on-surface)',
                  textAlign: 'left',
                  cursor: 'pointer',
                  display: 'flex',
                  flexDirection: 'column',
                  gap: '8px',
                }}
              >
                <div
                  style={{
                    width: '32px',
                    height: '32px',
                    borderRadius: 'var(--radius-sm)',
                    backgroundColor: 'var(--md-sys-color-surface)',
                    color: 'var(--md-sys-color-primary)',
                    display: 'flex',
                    alignItems: 'center',
                    justifyContent: 'center',
                  }}
                >
                  <Icon name={act.icon} size={18} />
                </div>
                <div>
                  <div style={{ fontSize: '13px', fontWeight: 700 }}>{act.label}</div>
                  <div style={{ fontSize: '11px', color: 'var(--md-sys-color-on-surface-variant)' }}>{act.desc}</div>
                </div>
              </motion.button>
            ))}
          </div>
        </div>

        {/* Live Activity Stream Feed */}
        <div
          style={{
            borderRadius: 'var(--radius-xl)',
            backgroundColor: 'var(--md-sys-color-surface)',
            border: '1px solid var(--md-sys-color-border)',
            padding: '24px 28px',
            boxShadow: 'var(--md-sys-elevation-level1)',
          }}
        >
          <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '16px' }}>
            <h3 style={{ margin: 0, fontSize: '17px', fontWeight: 600 }}>Activity Log & Audit Trail</h3>
            <span style={{ fontSize: '12px', color: 'var(--md-sys-color-primary)', fontWeight: 600 }}>All Activities ({activities.length})</span>
          </div>

          <div style={{ display: 'flex', flexDirection: 'column', gap: '16px' }}>
            {activities.map((act) => (
              <div
                key={act.id}
                style={{
                  display: 'flex',
                  alignItems: 'center',
                  justifyContent: 'space-between',
                  paddingBottom: '12px',
                  borderBottom: '1px solid var(--md-sys-color-border)',
                }}
              >
                <div style={{ display: 'flex', alignItems: 'center', gap: '12px' }}>
                  <div
                    style={{
                      width: '36px',
                      height: '36px',
                      borderRadius: 'var(--radius-pill)',
                      backgroundColor: act.iconBg,
                      color: 'var(--md-sys-color-primary)',
                      display: 'flex',
                      alignItems: 'center',
                      justifyContent: 'center',
                    }}
                  >
                    <Icon name={act.icon} size={18} />
                  </div>
                  <div>
                    <div style={{ fontSize: '13px' }}>
                      <strong>{act.user}</strong> {act.action} <span style={{ color: 'var(--md-sys-color-primary)', fontWeight: 600 }}>{act.target}</span>
                    </div>
                    <div style={{ fontSize: '11px', color: 'var(--md-sys-color-on-surface-variant)' }}>{act.time}</div>
                  </div>
                </div>

                <Button variant="text" size="sm">Details</Button>
              </div>
            ))}
          </div>
        </div>
      </div>
    </div>
  );
};
