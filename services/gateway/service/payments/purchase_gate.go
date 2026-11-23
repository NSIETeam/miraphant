package payments

import (
	"errors"

	"github.com/songquanpeng/one-api/common"
	"github.com/songquanpeng/one-api/model"
	"github.com/songquanpeng/one-api/relay/apitype"
	"github.com/songquanpeng/one-api/relay/channeltype"
)

var ErrNoBillableModel = errors.New("no priced model has an enabled upstream channel")

func HasBillableModel(userID int) (bool, error) {
	group, err := model.GetUserGroup(userID)
	if err != nil {
		return false, err
	}
	var supportedTypes []int
	for channel := channeltype.OpenAI; channel <= channeltype.Dummy; channel++ {
		if channeltype.ToAPIType(channel) == apitype.OpenAI {
			supportedTypes = append(supportedTypes, channel)
		}
	}
	groupColumn := "abilities.`group`"
	if common.UsingPostgreSQL {
		groupColumn = `abilities."group"`
	}
	var count int64
	err = model.DB.Model(&model.PointActivePrice{}).
		Joins("JOIN point_price_versions ON point_price_versions.model_id = point_active_prices.model_id AND point_price_versions.version = point_active_prices.version").
		Joins("JOIN abilities ON abilities.model = point_active_prices.model_id AND abilities.enabled = ? AND "+groupColumn+" = ?", true, group).
		Joins("JOIN channels ON channels.id = abilities.channel_id AND channels.status = ? AND channels.type IN ?", model.ChannelStatusEnabled, supportedTypes).
		Count(&count).Error
	return count > 0, err
}
