import React, { useEffect, useRef, useState } from 'react';
import { Button, Form, Header, Message, Segment } from 'semantic-ui-react';
import { Link, useNavigate, useParams } from 'react-router-dom';
import { StrictAPI as API, showError, showSuccess } from '../../helpers';
import { renderQuotaWithPrompt } from '../../helpers/render';

const EditUser = () => {
  const params = useParams();
  const userId = params.id;
  const [loading, setLoading] = useState(true);
  const [loadError, setLoadError] = useState(false);
  const [loaded, setLoaded] = useState(false);
  const [loadedUserKey, setLoadedUserKey] = useState('');
  const [reloadVersion, setReloadVersion] = useState(0);
  const [pointsMode, setPointsMode] = useState(null);
  const [saving, setSaving] = useState(false);
  const originalQuota = useRef(null);
  const [inputs, setInputs] = useState({
    username: '',
    display_name: '',
    password: '',
    github_id: '',
    wechat_id: '',
    email: '',
    quota: 0,
    group: 'default'
  });
  const [groupOptions, setGroupOptions] = useState([]);
  const { username, display_name, password, github_id, wechat_id, email, quota } =
    inputs;
  const handleInputChange = (e, { name, value }) => {
    setInputs((inputs) => ({ ...inputs, [name]: value }));
  };
  const navigate = useNavigate();
  const routeUserKey = userId || 'self';
  const handleCancel = () => {
    navigate(userId ? '/admin/users' : '/console/profile');
  }
  useEffect(() => {
    let active = true;
    setLoading(true);
    setLoadError(false);
    setLoaded(false);
    setLoadedUserKey('');
    setPointsMode(null);
    const loadUser = async () => {
      try {
        const [res, statusRes] = await Promise.all([
          userId ? API.get(`/api/user/${userId}`) : API.get('/api/user/self'),
          API.get('/api/status')
        ]);
        const data = res?.data?.data;
        const status = statusRes?.data?.data;
        if (!res?.data?.success || !data || !statusRes?.data?.success || typeof status?.points_billing_enabled !== 'boolean') {
          throw new Error('服务端没有返回完整的用户与计费模式信息');
        }
        if (!active) return;
        originalQuota.current = data.quota;
        data.password = '';
        setInputs(data);
        setPointsMode(status.points_billing_enabled);
        setLoadedUserKey(routeUserKey);
        setLoaded(true);
      } catch (_) {
        if (!active) return;
        setLoadError(true);
        setLoaded(false);
        showError('用户资料或计费模式读取失败，请检查管理权限后重试。');
      } finally {
        if (active) setLoading(false);
      }
    };
    void loadUser();
    return () => { active = false; };
  }, [routeUserKey, reloadVersion, userId]);
  useEffect(() => {
    if (!userId) return;
    let active = true;
    API.get('/api/group/').then((res) => {
      if (!active) return;
      setGroupOptions((res?.data?.data || []).map((group) => ({ key: group, text: group, value: group })));
    }).catch(() => {
      if (active) showError('分组列表读取失败，分组暂时无法编辑。');
    });
    return () => { active = false; };
  }, [userId]);

  const submit = async () => {
    if (!loaded || loadedUserKey !== routeUserKey) {
      showError('当前用户资料尚未读取完成，请重试。');
      return;
    }
    setSaving(true);
    try {
      let res;
      if (userId) {
        let data = { ...inputs, id: parseInt(userId) };
        if (pointsMode && originalQuota.current !== null) {
          // UpdateUser still expects the legacy projection to remain unchanged.
          data.quota = originalQuota.current;
        } else if (typeof data.quota === 'string') {
          data.quota = parseInt(data.quota);
        }
        res = await API.put(`/api/user/`, data);
      } else {
        res = await API.put(`/api/user/self`, inputs);
      }
      const { success, message } = res.data;
      if (success) {
        showSuccess('用户信息更新成功！');
      } else {
        showError(message || '用户信息更新失败');
      }
    } catch (_) {
      showError('用户信息更新失败，请检查权限后重试。');
    } finally {
      setSaving(false);
    }
  };

  if (loading || (loaded && loadedUserKey !== routeUserKey)) return <Segment loading><Header as='h3'>正在读取用户资料…</Header></Segment>;
  if (loadError || !loaded || pointsMode === null) return <Segment>
    <Header as='h3'>无法编辑用户资料</Header>
    <Message negative>用户信息或服务计费模式未能读取。为避免提交默认值，当前不显示可编辑表单。</Message>
    <Button onClick={() => setReloadVersion((version) => version + 1)}>重新读取</Button>
  </Segment>;

  return (
    <>
      <Segment loading={saving}>
        <Header as='h3'>更新用户信息</Header>
        {pointsMode && <Message info>
          当前使用积分账本。本页只维护资料和分组，不修改旧额度；客户积分流水请前往 <Link to='/admin/users'>客户与积分</Link>。
        </Message>}
        <Form autoComplete='new-password'>
          <Form.Field>
            <Form.Input
              label='用户名'
              name='username'
              placeholder={'请输入新的用户名'}
              onChange={handleInputChange}
              value={username}
              autoComplete='new-password'
            />
          </Form.Field>
          <Form.Field>
            <Form.Input
              label='密码'
              name='password'
              type={'password'}
              placeholder={'请输入新的密码，最短 8 位'}
              onChange={handleInputChange}
              value={password}
              autoComplete='new-password'
            />
          </Form.Field>
          <Form.Field>
            <Form.Input
              label='显示名称'
              name='display_name'
              placeholder={'请输入新的显示名称'}
              onChange={handleInputChange}
              value={display_name}
              autoComplete='new-password'
            />
          </Form.Field>
          {
            userId && <>
              <Form.Field>
                <Form.Dropdown
                  label='分组'
                  placeholder={'请选择分组'}
                  name='group'
                  fluid
                  search
                  selection
                  allowAdditions
                  additionLabel={pointsMode ? '新分组需先配置可用渠道权限。' : '请在系统设置页面编辑分组倍率以添加新的分组：'}
                  onChange={handleInputChange}
                  value={inputs.group}
                  autoComplete='new-password'
                  options={groupOptions}
                />
              </Form.Field>
              {pointsMode ? null : <Form.Field>
                <Form.Input
                  label={`剩余额度${renderQuotaWithPrompt(quota)}`}
                  name='quota'
                  placeholder={'请输入新的剩余额度'}
                  onChange={handleInputChange}
                  value={quota}
                  type={'number'}
                  autoComplete='new-password'
                />
              </Form.Field>}
            </>
          }
          <Form.Field>
            <Form.Input
              label='已绑定的 GitHub 账户'
              name='github_id'
              value={github_id}
              autoComplete='new-password'
              placeholder='此项只读，需要用户通过个人设置页面的相关绑定按钮进行绑定，不可直接修改'
              readOnly
            />
          </Form.Field>
          <Form.Field>
            <Form.Input
              label='已绑定的微信账户'
              name='wechat_id'
              value={wechat_id}
              autoComplete='new-password'
              placeholder='此项只读，需要用户通过个人设置页面的相关绑定按钮进行绑定，不可直接修改'
              readOnly
            />
          </Form.Field>
          <Form.Field>
            <Form.Input
              label='已绑定的邮箱账户'
              name='email'
              value={email}
              autoComplete='new-password'
              placeholder='此项只读，需要用户通过个人设置页面的相关绑定按钮进行绑定，不可直接修改'
              readOnly
            />
          </Form.Field>
          <Button onClick={handleCancel}>取消</Button>
          <Button positive onClick={submit} disabled={saving}>{saving ? '保存中…' : '提交'}</Button>
        </Form>
      </Segment>
    </>
  );
};

export default EditUser;
