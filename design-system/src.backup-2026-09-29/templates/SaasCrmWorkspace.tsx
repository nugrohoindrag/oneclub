/**
 * @license MIT
 * SaasCrmWorkspace Component — Morphic Design System
 * 
 * Avatar Photography Assets: Unsplash Open License (Royalty-free community portraits)
 */

import React, { useState } from 'react';
import { motion } from 'motion/react';
import { Icon } from '../components/communication/Icon.js';
import { Button } from '../components/actions/Button.js';
import { IconButton } from '../components/actions/IconButton.js';
import { Switch } from '../components/selection/index.js';

export interface TeamMember {
  id: string;
  name: string;
  email: string;
  role: 'Super Admin' | 'Product Lead' | 'Senior Engineer' | 'UI/UX Designer';
  status: 'Active' | 'Invited' | 'Suspended';
  twoFactorEnabled: boolean;
  avatarUrl: string;
}

export const SaasCrmWorkspace: React.FC<{
  onInviteMember?: () => void;
}> = ({ onInviteMember }) => {
  const [members, setMembers] = useState<TeamMember[]>([
    {
      id: '1',
      name: 'Alexander Pratama',
      email: 'alex.pratama@example.com',
      role: 'Super Admin',
      status: 'Active',
      twoFactorEnabled: true,
      avatarUrl: 'https://images.unsplash.com/photo-1534528741775-53994a69daeb?w=150&auto=format&fit=crop&q=80',
    },
    {
      id: '2',
      name: 'Nadia Salsabila',
      email: 'nadia.s@example.com',
      role: 'Product Lead',
      status: 'Active',
      twoFactorEnabled: true,
      avatarUrl: 'https://images.unsplash.com/photo-1494790108377-be9c29b29330?w=150&auto=format&fit=crop&q=80',
    },
    {
      id: '3',
      name: 'Dimas Wicaksono',
      email: 'dimas.w@example.com',
      role: 'Senior Engineer',
      status: 'Active',
      twoFactorEnabled: false,
      avatarUrl: 'https://images.unsplash.com/photo-1507003211169-0a1dd7228f2d?w=150&auto=format&fit=crop&q=80',
    },
    {
      id: '4',
      name: 'Clarissa Maharani',
      email: 'clarissa.m@example.com',
      role: 'UI/UX Designer',
      status: 'Invited',
      twoFactorEnabled: false,
      avatarUrl: 'https://images.unsplash.com/photo-1517841905240-472988babdf9?w=150&auto=format&fit=crop&q=80',
    },
  ]);

  const toggle2FA = (id: string) => {
    setMembers((prev) =>
      prev.map((m) => (m.id === id ? { ...m, twoFactorEnabled: !m.twoFactorEnabled } : m))
    );
  };

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: '28px' }}>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
        <div>
          <h1 style={{ margin: '0 0 4px', fontSize: '26px', fontWeight: 700 }}>
            Team Management & Access Control
          </h1>
          <p style={{ margin: 0, color: 'var(--md-sys-color-on-surface-variant)', fontSize: '13px' }}>
            Manage user permissions, 2FA security authentication, and enterprise collaboration.
          </p>
        </div>
        <Button variant="filled" icon={<Icon name="person_add" />} onClick={onInviteMember}>
          Invite Team Member
        </Button>
      </div>

      {/* Team Directory Table Card */}
      <div
        style={{
          borderRadius: 'var(--radius-xl)',
          backgroundColor: 'var(--md-sys-color-surface)',
          border: '1px solid var(--md-sys-color-border)',
          overflow: 'hidden',
          boxShadow: 'var(--md-sys-elevation-level1)',
        }}
      >
        <div style={{ padding: '20px 24px', borderBottom: '1px solid var(--md-sys-color-border)', display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
          <span style={{ fontSize: '14px', fontWeight: 700 }}>Active Members Directory ({members.length})</span>
          <div style={{ fontSize: '12px', color: 'var(--md-sys-color-on-surface-variant)' }}>Organization: Acme Platform</div>
        </div>

        <div>
          {members.map((m) => (
            <div
              key={m.id}
              style={{
                display: 'flex',
                alignItems: 'center',
                justifyContent: 'space-between',
                padding: '16px 24px',
                borderBottom: '1px solid var(--md-sys-color-border)',
              }}
            >
              {/* Left: Avatar Photo & Name */}
              <div style={{ display: 'flex', alignItems: 'center', gap: '14px' }}>
                <div
                  style={{
                    width: '42px',
                    height: '42px',
                    borderRadius: 'var(--radius-pill)',
                    overflow: 'hidden',
                    backgroundColor: 'var(--md-sys-color-surface-container)',
                    border: '1px solid var(--md-sys-color-border)',
                    flexShrink: 0,
                  }}
                >
                  <img
                    src={m.avatarUrl}
                    alt={m.name}
                    style={{ width: '100%', height: '100%', objectFit: 'cover' }}
                  />
                </div>
                <div>
                  <div style={{ fontSize: '14px', fontWeight: 600 }}>{m.name}</div>
                  <div style={{ fontSize: '12px', color: 'var(--md-sys-color-on-surface-variant)' }}>{m.email}</div>
                </div>
              </div>

              {/* Middle: Role & Status */}
              <div style={{ display: 'flex', alignItems: 'center', gap: '20px' }}>
                <span
                  style={{
                    padding: '4px 12px',
                    borderRadius: 'var(--radius-pill)',
                    backgroundColor: 'var(--md-sys-color-surface-container)',
                    fontSize: '12px',
                    fontWeight: 600,
                  }}
                >
                  {m.role}
                </span>

                <span
                  style={{
                    padding: '4px 10px',
                    borderRadius: 'var(--radius-pill)',
                    backgroundColor: m.status === 'Active' ? 'var(--md-sys-color-success-container)' : 'var(--md-sys-color-warning-container)',
                    color: m.status === 'Active' ? 'var(--md-sys-color-primary)' : 'var(--md-sys-color-warning)',
                    fontSize: '11px',
                    fontWeight: 600,
                  }}
                >
                  {m.status}
                </span>
              </div>

              {/* Right: 2FA Toggle & Action */}
              <div style={{ display: 'flex', alignItems: 'center', gap: '24px' }}>
                <div style={{ display: 'flex', alignItems: 'center', gap: '8px' }}>
                  <span style={{ fontSize: '12px', color: 'var(--md-sys-color-on-surface-variant)' }}>2FA:</span>
                  <Switch checked={m.twoFactorEnabled} onChange={() => toggle2FA(m.id)} />
                </div>
                <IconButton variant="standard" icon={<Icon name="more_vert" />} />
              </div>
            </div>
          ))}
        </div>
      </div>
    </div>
  );
};
